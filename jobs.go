package main

import (
	"context"
	"sort"
	"sync"
	"time"
)

// jobStatus es un "enum" a la manera de Go: un tipo string con constantes.
type jobStatus string

const (
	statusRunning  jobStatus = "running"
	statusDone     jobStatus = "done"
	statusFailed   jobStatus = "failed"
	statusCanceled jobStatus = "canceled"
)

// job es un envío desde la ventana: uno o varios enlaces con sus opciones.
// Las etiquetas `json:"..."` indican cómo se llama cada campo al serializar.
// Los campos en minúscula (cancel) no se exportan ni se serializan.
type job struct {
	ID      int       `json:"id"`
	URLs    []string  `json:"urls"`
	Format  string    `json:"format"`
	Status  jobStatus `json:"status"`
	Started time.Time `json:"started"`
	Lines   []string  `json:"lines"`
	OK      int       `json:"ok"`
	Failed  int       `json:"failed"`
	cancel  context.CancelFunc
}

const maxLogLines = 300

// jobStore guarda los trabajos en memoria. Varias goroutines (las llamadas
// desde la ventana y las descargas) lo tocan a la vez, por eso todo acceso
// pasa por el mutex. Regla de oro: quien coge el lock lo suelta con defer.
//
// onChange se llama (fuera del lock) cada vez que algo cambia, para que la
// ventana se refresque sin tener que preguntar cada X segundos.
type jobStore struct {
	mu       sync.Mutex
	nextID   int
	jobs     map[int]*job
	onChange func()
	onFinish func(paths []string) // al acabar un trabajo, con todos sus ficheros finales (opcional)
}

func newJobStore(onChange func()) *jobStore {
	return &jobStore{nextID: 1, jobs: make(map[int]*job), onChange: onChange}
}

// add crea el trabajo y lanza la descarga en una goroutine: la llamada
// vuelve al instante y la ventana recibe el progreso por eventos.
func (s *jobStore) add(urls []string, cfg config) *job {
	ctx, cancel := context.WithCancel(context.Background())

	s.mu.Lock()
	j := &job{
		ID:      s.nextID,
		URLs:    urls,
		Format:  cfg.format,
		Status:  statusRunning,
		Started: time.Now(),
		cancel:  cancel,
	}
	s.nextID++
	s.jobs[j.ID] = j
	s.mu.Unlock()
	s.onChange()

	go func() {
		results := downloadAll(ctx, urls, cfg, func(line string) { s.appendLine(j.ID, line) })

		s.mu.Lock()
		for _, r := range results {
			if r.err != nil {
				j.Failed++
			} else {
				j.OK++
			}
		}
		switch {
		case ctx.Err() != nil:
			j.Status = statusCanceled
		case j.Failed > 0:
			j.Status = statusFailed
		default:
			j.Status = statusDone
		}
		s.mu.Unlock()
		s.onChange()
		if s.onFinish != nil {
			var paths []string
			for _, r := range results {
				paths = append(paths, r.paths...)
			}
			s.onFinish(paths)
		}
	}()

	return j
}

func (s *jobStore) appendLine(id int, line string) {
	s.mu.Lock()
	j, ok := s.jobs[id]
	if ok {
		j.Lines = append(j.Lines, line)
		if len(j.Lines) > maxLogLines {
			j.Lines = j.Lines[len(j.Lines)-maxLogLines:]
		}
	}
	s.mu.Unlock()
	if ok {
		s.onChange()
	}
}

// snapshot devuelve una copia de los trabajos, del más reciente al más
// antiguo. Copiamos para que la serialización a JSON ocurra fuera del lock.
func (s *jobStore) snapshot() []job {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]job, 0, len(s.jobs))
	for _, j := range s.jobs {
		cp := *j
		cp.Lines = append([]string(nil), j.Lines...)
		out = append(out, cp)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID > out[b].ID })
	return out
}

func (s *jobStore) cancelJob(id int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok || j.Status != statusRunning {
		return false
	}
	j.cancel()
	return true
}

// cancelAll se usa al cerrar la app para no dejar yt-dlp huérfanos.
func (s *jobStore) cancelAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		if j.Status == statusRunning {
			j.cancel()
		}
	}
}
