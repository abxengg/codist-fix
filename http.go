package fix

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
)

// Routes registers the requests the interface's fix dialog sends:
//
//	POST   /api/fix              prepare a fix, optionally the way the developer suggested
//	POST   /api/fix/{id}/check   build it and run the tests in a separate copy
//	POST   /api/fix/{id}/apply   write it and commit only its files
//	DELETE /api/fix/{id}         forget it
//	POST   /api/memory           remember why a fix was rejected
func (f *Fixer) Routes(handle func(pattern string, h func(http.ResponseWriter, *http.Request))) {
	handle("POST /api/fix", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			SHA      string `json:"sha"`
			Path     string `json:"path"`
			Line     int    `json:"line"`
			Title    string `json:"title"`
			Guidance string `json:"guidance"`
			Also     []Ref  `json:"also"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.SHA == "" || in.Path == "" || in.Title == "" {
			fail(w, http.StatusBadRequest, "Send the commit, file, line and title of the issue.")
			return
		}
		p, err := f.Propose(r.Context(), in.SHA, in.Path, in.Line, in.Title, in.Guidance, in.Also...)
		var refused Refused
		switch {
		case errors.As(err, &refused):
			slog.Info("fix refused", "lib", "fix", "sha", in.SHA, "path", in.Path, "title", in.Title, "why", refused.Reason)
			reply(w, map[string]any{"blocked": true, "error": refused.Reason})
		case err != nil:
			slog.Warn("fix not prepared", "lib", "fix", "sha", in.SHA, "path", in.Path, "title", in.Title, "err", err)
			fail(w, http.StatusUnprocessableEntity, err.Error())
		default:
			slog.Info("fix prepared", "lib", "fix", "id", p.ID, "sha", in.SHA, "title", in.Title, "files", len(p.Paths))
			reply(w, p)
		}
	})
	handle("POST /api/fix/{id}/check", func(w http.ResponseWriter, r *http.Request) {
		c := f.Check(r.Context(), r.PathValue("id"))
		slog.Info("fix checked", "lib", "fix", "id", r.PathValue("id"), "status", c.Status, "step", c.Step, "took_ms", c.TookMs)
		reply(w, c)
	})
	handle("POST /api/fix/{id}/apply", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		sha, err := f.Apply(r.PathValue("id"), in.Message)
		if err != nil {
			slog.Warn("fix not applied", "lib", "fix", "id", r.PathValue("id"), "err", err)
			fail(w, http.StatusConflict, err.Error())
			return
		}
		slog.Info("fix applied", "lib", "fix", "id", r.PathValue("id"), "commit", sha)
		reply(w, map[string]string{"commit": sha})
	})
	handle("DELETE /api/fix/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.Discard(r.PathValue("id"))
		reply(w, map[string]bool{"ok": true})
	})
	handle("POST /api/memory", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Title string `json:"title"`
			Path  string `json:"path"`
			Text  string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if err := f.Remember(Note(in)); err != nil {
			slog.Warn("rejection note not saved", "lib", "fix", "err", err)
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		reply(w, map[string]string{"id": newID(), "text": in.Text})
	})
}

func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
