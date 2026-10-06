package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/swarm"
	"github.com/docker/docker/client"
)

// fakeSwarm is a stateful, in-memory stand-in for the Docker Engine API's
// service/secret/config/network endpoints - enough for handler tests that
// care about what ends up deployed (create a service, rotate a secret,
// deploy and prune a stack) rather than about one canned response.
type fakeSwarm struct {
	mu       sync.Mutex
	nextID   int
	services map[string]*swarm.Service // by ID
	secrets  map[string]*swarm.Secret
	configs  map[string]*swarm.Config
	networks []network.Summary
}

func newFakeSwarm() *fakeSwarm {
	return &fakeSwarm{
		services: map[string]*swarm.Service{},
		secrets:  map[string]*swarm.Secret{},
		configs:  map[string]*swarm.Config{},
	}
}

func (f *fakeSwarm) id(prefix string) string {
	f.nextID++
	return fmt.Sprintf("%s%d", prefix, f.nextID)
}

func (f *fakeSwarm) addService(spec swarm.ServiceSpec) *swarm.Service {
	f.mu.Lock()
	defer f.mu.Unlock()
	svc := &swarm.Service{ID: f.id("svc"), Spec: spec}
	svc.Version.Index = 1
	f.services[svc.ID] = svc
	return svc
}

func (f *fakeSwarm) addSecret(name string) *swarm.Secret {
	f.mu.Lock()
	defer f.mu.Unlock()
	sec := &swarm.Secret{ID: f.id("sec"), Spec: swarm.SecretSpec{Annotations: swarm.Annotations{Name: name}}}
	f.secrets[sec.ID] = sec
	return sec
}

func (f *fakeSwarm) addConfig(name, data string) *swarm.Config {
	f.mu.Lock()
	defer f.mu.Unlock()
	cfg := &swarm.Config{ID: f.id("cfg"), Spec: swarm.ConfigSpec{Annotations: swarm.Annotations{Name: name}, Data: []byte(data)}}
	f.configs[cfg.ID] = cfg
	return cfg
}

// service returns the service with the given name, or nil.
func (f *fakeSwarm) service(name string) *swarm.Service {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.findService(name)
}

func (f *fakeSwarm) findService(idOrName string) *swarm.Service {
	if svc, ok := f.services[idOrName]; ok {
		return svc
	}
	for _, svc := range f.services {
		if svc.Spec.Name == idOrName {
			return svc
		}
	}
	return nil
}

func (f *fakeSwarm) client(t *testing.T) *client.Client {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /services", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		out := []swarm.Service{}
		for _, svc := range f.services {
			out = append(out, *svc)
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("GET /services/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		svc := f.findService(r.PathValue("id"))
		if svc == nil {
			http.Error(w, `{"message":"service not found"}`, http.StatusNotFound)
			return
		}
		writeJSON(w, svc)
	})
	mux.HandleFunc("POST /services/create", func(w http.ResponseWriter, r *http.Request) {
		var spec swarm.ServiceSpec
		if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		svc := f.addService(spec)
		writeJSON(w, swarm.ServiceCreateResponse{ID: svc.ID})
	})
	mux.HandleFunc("POST /services/{id}/update", func(w http.ResponseWriter, r *http.Request) {
		var spec swarm.ServiceSpec
		if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		svc := f.findService(r.PathValue("id"))
		if svc == nil {
			http.Error(w, `{"message":"service not found"}`, http.StatusNotFound)
			return
		}
		svc.Spec = spec
		svc.Version.Index++
		writeJSON(w, swarm.ServiceUpdateResponse{})
	})
	mux.HandleFunc("DELETE /services/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if svc := f.findService(r.PathValue("id")); svc != nil {
			delete(f.services, svc.ID)
		}
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("GET /networks", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		writeJSON(w, append([]network.Summary{}, f.networks...))
	})
	mux.HandleFunc("POST /networks/create", func(w http.ResponseWriter, r *http.Request) {
		var req network.CreateRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		defer f.mu.Unlock()
		n := network.Summary{ID: f.id("net"), Name: req.Name, Labels: req.Labels}
		f.networks = append(f.networks, n)
		writeJSON(w, network.CreateResponse{ID: n.ID})
	})

	mux.HandleFunc("GET /secrets", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		out := []swarm.Secret{}
		for _, sec := range f.secrets {
			out = append(out, *sec)
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("GET /secrets/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		sec, ok := f.secrets[r.PathValue("id")]
		if !ok {
			http.Error(w, `{"message":"secret not found"}`, http.StatusNotFound)
			return
		}
		writeJSON(w, sec)
	})
	mux.HandleFunc("POST /secrets/create", func(w http.ResponseWriter, r *http.Request) {
		var spec swarm.SecretSpec
		_ = json.NewDecoder(r.Body).Decode(&spec)
		sec := f.addSecret(spec.Name)
		writeJSON(w, swarm.SecretCreateResponse{ID: sec.ID})
	})
	mux.HandleFunc("DELETE /secrets/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		id := r.PathValue("id")
		for _, svc := range f.services {
			for _, ref := range svc.Spec.TaskTemplate.ContainerSpec.Secrets {
				if ref.SecretID == id {
					http.Error(w, `{"message":"secret is in use"}`, http.StatusBadRequest)
					return
				}
			}
		}
		delete(f.secrets, id)
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("GET /configs", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		out := []swarm.Config{}
		for _, cfg := range f.configs {
			out = append(out, *cfg)
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("GET /configs/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		cfg, ok := f.configs[r.PathValue("id")]
		if !ok {
			http.Error(w, `{"message":"config not found"}`, http.StatusNotFound)
			return
		}
		writeJSON(w, cfg)
	})
	mux.HandleFunc("POST /configs/create", func(w http.ResponseWriter, r *http.Request) {
		var spec swarm.ConfigSpec
		_ = json.NewDecoder(r.Body).Decode(&spec)
		cfg := f.addConfig(spec.Name, string(spec.Data))
		writeJSON(w, swarm.ConfigCreateResponse{ID: cfg.ID})
	})
	mux.HandleFunc("DELETE /configs/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(f.configs, r.PathValue("id"))
		w.WriteHeader(http.StatusNoContent)
	})

	return newFakeDocker(t, mux)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// postForm builds an admin POST request carrying form, with path values
// set the way the mux would have.
func postForm(path string, form url.Values, pathValues ...string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for i := 0; i+1 < len(pathValues); i += 2 {
		r.SetPathValue(pathValues[i], pathValues[i+1])
	}
	return withUser(r, testAdmin)
}

func swarmNetwork(id, name, stack string) network.Summary {
	return network.Summary{ID: id, Name: name, Labels: map[string]string{stackLabel: stack}}
}
