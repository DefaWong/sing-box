package clashapi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/provider/parser"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/service"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/render"
)

const maxManagedConfigBytes = 16 << 20

// managedConfigRouter is AtomDNS's narrow, authenticated configuration-file
// boundary. A save returns before Reload closes this HTTP connection; the new
// instance exposes the tracked outcome on the status sub-resource.
func managedConfigRouter(server *Server) http.Handler {
	r := chi.NewRouter()
	r.Get("/", getManagedConfig(server))
	r.Put("/", putManagedConfig(server))
	r.Get("/status", getManagedConfigStatus(server))
	r.Post("/parse-node", func(w http.ResponseWriter, r *http.Request) {
		parseManagedNode(server, w, r)
	})
	return r
}

func managedConfigStore(server *Server) (adapter.ConfigStore, bool) {
	store := service.FromContext[adapter.ConfigStore](server.ctx)
	return store, store != nil
}

func managedConfigRevision(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func getManagedConfig(server *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		store, loaded := managedConfigStore(server)
		if !loaded {
			render.Status(r, http.StatusNotImplemented)
			render.JSON(w, r, newError("configuration management requires a single writable configuration file"))
			return
		}
		content, err := store.Load()
		if err != nil {
			render.Status(r, http.StatusInternalServerError)
			render.JSON(w, r, newError(err.Error()))
			return
		}
		render.JSON(w, r, render.M{"revision": managedConfigRevision(content), "config": json.RawMessage(content)})
	}
}

func putManagedConfig(server *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		store, loaded := managedConfigStore(server)
		if !loaded {
			render.Status(r, http.StatusNotImplemented)
			render.JSON(w, r, newError("configuration management requires a single writable configuration file"))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxManagedConfigBytes)
		var request struct {
			Revision string          `json:"revision"`
			Config   json.RawMessage `json:"config"`
		}
		if err := render.DecodeJSON(r.Body, &request); err != nil {
			render.Status(r, http.StatusBadRequest)
			render.JSON(w, r, ErrBadRequest)
			return
		}
		revision, err := store.Save([]byte(request.Revision), request.Config)
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, adapter.ErrConfigConflict) || errors.Is(err, adapter.ErrConfigReloadInProgress) {
				status = http.StatusConflict
			}
			render.Status(r, status)
			render.JSON(w, r, newError(err.Error()))
			return
		}
		server.logger.Warn("sing-box reloading managed configuration...")
		// Closing the old Box also closes this HTTP server. Flush the ACK before
		// asking the process loop to replace it; status is polled on the new one.
		render.JSON(w, r, render.M{"revision": revision, "reloading": true})
		if err := http.NewResponseController(w).Flush(); err != nil {
			return
		}
		server.router.Reload()
	}
}

func getManagedConfigStatus(server *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tracker := service.FromContext[adapter.ManagedReloadResult](server.ctx)
		if tracker == nil {
			render.Status(r, http.StatusNotImplemented)
			render.JSON(w, r, newError("configuration reload status is unavailable"))
			return
		}
		revision, reloading, err := tracker.Status()
		response := render.M{"revision": revision, "reloading": reloading}
		if err != nil {
			response["error"] = err.Error()
		}
		render.JSON(w, r, response)
	}
}

func parseManagedNode(server *Server, w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var request struct {
		Link string `json:"link"`
		Tag  string `json:"tag"`
	}
	if err := render.DecodeJSON(r.Body, &request); err != nil {
		render.Status(r, http.StatusBadRequest)
		render.JSON(w, r, newError("invalid node link request"))
		return
	}
	link := strings.TrimSpace(request.Link)
	if link == "" || strings.ContainsAny(link, "\r\n") {
		render.Status(r, http.StatusBadRequest)
		render.JSON(w, r, newError("enter exactly one node link"))
		return
	}
	outbound, err := parser.ParseSubscriptionLink(link)
	if err != nil {
		render.Status(r, http.StatusBadRequest)
		render.JSON(w, r, newError(err.Error()))
		return
	}
	if request.Tag != "" {
		outbound.Tag = strings.TrimSpace(request.Tag)
	}
	if outbound.Tag == "" {
		render.Status(r, http.StatusBadRequest)
		render.JSON(w, r, newError("node tag is required (use the link fragment or tag field)"))
		return
	}
	if len(outbound.Tag) > 255 || strings.ContainsAny(outbound.Tag, "/\r\n\t ") {
		render.Status(r, http.StatusBadRequest)
		render.JSON(w, r, newError("node tag cannot contain whitespace or /"))
		return
	}
	content, err := outbound.MarshalJSONContext(server.ctx)
	if err != nil {
		render.Status(r, http.StatusInternalServerError)
		render.JSON(w, r, newError(err.Error()))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(content)
}
