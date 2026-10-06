package admin

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/swarm"
)

// newServiceForm is the top of the New service form - the fields the
// advanced editor doesn't have, since they're fixed once a service exists
// (name, stack) or have their own controls on its page (image, replicas).
type newServiceForm struct {
	Name     string
	Stack    string
	Image    string
	Mode     string // "replicated" or "global"
	Replicas string
}

// handleServiceNewPage serves the New service form: empty, or with
// ?from=<service> pre-filled from an existing service ("Clone").
func (s *Server) handleServiceNewPage(w http.ResponseWriter, r *http.Request) {
	form := newServiceForm{Mode: "replicated", Replicas: "1"}
	editor := editorFormData{RestartCondition: "any", UpdateOrder: "stop-first", UpdateFailure: "pause"}

	if from := r.URL.Query().Get("from"); from != "" {
		svc, err := s.getService(r.Context(), from)
		if err != nil {
			http.Error(w, "get service: "+err.Error(), http.StatusNotFound)
			return
		}
		nets, _ := s.docker.NetworkList(r.Context(), network.ListOptions{})
		netNames := make(map[string]string, len(nets))
		for _, n := range nets {
			netNames[n.ID] = n.Name
		}
		editor = serviceEditorForm(svc, netNames)
		form.Stack = stackName(svc)
		form.Name = strings.TrimPrefix(svc.Spec.Name, form.Stack+"_") + "-copy"
		if cs := svc.Spec.TaskTemplate.ContainerSpec; cs != nil {
			form.Image = imageTag(cs.Image)
		}
		if svc.Spec.Mode.Global != nil {
			form.Mode = "global"
		} else {
			form.Replicas = strconv.FormatUint(replicasOf(svc.Spec), 10)
		}
		// Published ports can't be shared between two services; a clone
		// that kept them would just fail to create.
		editor.Ports = ""
	}
	s.renderNewService(w, r, form, editor, "")
}

func (s *Server) renderNewService(w http.ResponseWriter, r *http.Request, form newServiceForm, editor editorFormData, errMsg string) {
	s.render(w, r, "service_new.html", map[string]any{
		"User":   userFromContext(r),
		"Form":   form,
		"Editor": editor,
		"Error":  errMsg,
	})
}

// handleServiceCreate creates a service from the New service form. A stack
// name, if given, prefixes the service name and labels it as part of that
// stack, exactly like a compose deploy would.
func (s *Server) handleServiceCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	form := newServiceForm{
		Name:     strings.TrimSpace(r.FormValue("name")),
		Stack:    strings.TrimSpace(r.FormValue("stack")),
		Image:    strings.TrimSpace(r.FormValue("image")),
		Mode:     orDefault(r.FormValue("mode"), "replicated"),
		Replicas: r.FormValue("replicas"),
	}
	fail := func(msg string) {
		s.renderNewService(w, r, form, editorFormFromRequest(r), msg)
	}

	spec, err := s.newServiceSpec(r, form)
	if err != nil {
		fail(err.Error())
		return
	}

	auth, err := s.encodedRegistryAuthFor(form.Image)
	if err != nil {
		fail("registry credential: " + err.Error())
		return
	}
	if _, err := s.docker.ServiceCreate(r.Context(), spec, swarm.ServiceCreateOptions{
		EncodedRegistryAuth: auth,
		QueryRegistry:       true,
	}); err != nil {
		s.audit(r, "service.create", spec.Name, "", err)
		fail("create service: " + err.Error())
		return
	}
	s.audit(r, "service.create", spec.Name, "image="+form.Image, nil)
	redirect(w, r, "/services/"+spec.Name)
}

func (s *Server) newServiceSpec(r *http.Request, form newServiceForm) (swarm.ServiceSpec, error) {
	if !stackNamePattern.MatchString(form.Name) {
		return swarm.ServiceSpec{}, fmt.Errorf("invalid service name (letters, digits, and _ . - after the first character)")
	}
	if form.Stack != "" && !stackNamePattern.MatchString(form.Stack) {
		return swarm.ServiceSpec{}, fmt.Errorf("invalid stack name")
	}
	if form.Image == "" {
		return swarm.ServiceSpec{}, fmt.Errorf("image is required")
	}

	spec := swarm.ServiceSpec{
		Annotations:  swarm.Annotations{Name: form.Name},
		TaskTemplate: swarm.TaskSpec{ContainerSpec: &swarm.ContainerSpec{Image: form.Image}},
	}
	if form.Stack != "" {
		spec.Name = form.Stack + "_" + form.Name
		spec.Labels = map[string]string{stackLabel: form.Stack}
	}

	switch form.Mode {
	case "global":
		spec.Mode = swarm.ServiceMode{Global: &swarm.GlobalService{}}
	case "replicated":
		replicas, err := strconv.ParseUint(orDefault(form.Replicas, "1"), 10, 64)
		if err != nil {
			return spec, fmt.Errorf("invalid replicas %q", form.Replicas)
		}
		spec.Mode = swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: &replicas}}
	default:
		return spec, fmt.Errorf("invalid mode %q", form.Mode)
	}

	if err := s.applyEditorForm(r.Context(), r, &spec); err != nil {
		return spec, err
	}
	return spec, nil
}
