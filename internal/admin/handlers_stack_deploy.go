package admin

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"swarmdash/internal/compose"
)

var stackNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

func (s *Server) handleStackDeployPage(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{"User": userFromContext(r)}
	if id := r.URL.Query().Get("template"); id != "" {
		if tpl, ok := findTemplate(id); ok {
			data["Name"] = tpl.ID
			data["Compose"] = tpl.Compose
		}
	}
	s.render(w, r, "stack_deploy.html", data)
}

func (s *Server) handleTemplatesPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "templates_gallery.html", map[string]any{
		"User":      userFromContext(r),
		"Templates": builtinTemplates,
	})
}

func (s *Server) handleStackDeployPreview(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name := r.FormValue("name")
	yamlText := r.FormValue("compose")

	data := map[string]any{"User": userFromContext(r), "Name": name, "Compose": yamlText}

	if !stackNamePattern.MatchString(name) {
		data["Error"] = "invalid stack name"
		s.render(w, r, "stack_deploy.html", data)
		return
	}
	file, err := compose.Parse([]byte(yamlText))
	if err != nil {
		data["Error"] = err.Error()
		s.render(w, r, "stack_deploy.html", data)
		return
	}
	diffs, err := s.previewCompose(r.Context(), name, file)
	if err != nil {
		data["Error"] = err.Error()
		s.render(w, r, "stack_deploy.html", data)
		return
	}
	data["Diffs"] = diffs
	s.render(w, r, "stack_deploy.html", data)
}

func (s *Server) handleStackDeploySubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name := r.FormValue("name")
	yamlText := r.FormValue("compose")

	if !stackNamePattern.MatchString(name) {
		s.render(w, r, "stack_deploy.html", map[string]any{
			"User": userFromContext(r), "Error": "invalid stack name", "Name": name, "Compose": yamlText,
		})
		return
	}

	file, err := compose.Parse([]byte(yamlText))
	if err != nil {
		s.render(w, r, "stack_deploy.html", map[string]any{
			"User": userFromContext(r), "Error": err.Error(), "Name": name, "Compose": yamlText,
		})
		return
	}

	result, err := s.applyCompose(r.Context(), name, file)
	if err != nil {
		s.audit(r, "stack.deploy", name, "", err)
		s.render(w, r, "stack_deploy.html", map[string]any{
			"User": userFromContext(r), "Error": err.Error(), "Name": name, "Compose": yamlText,
		})
		return
	}

	s.audit(r, "stack.deploy", name, fmt.Sprintf("created=%d updated=%d", len(result.Created), len(result.Updated)), nil)
	redirect(w, r, "/stacks/"+name)
}

func (s *Server) handleStackExport(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	services, err := s.listServices(r.Context())
	if err != nil {
		http.Error(w, "list services: "+err.Error(), http.StatusBadGateway)
		return
	}

	out := map[string]compose.Service{}
	for _, svc := range services {
		if stackName(svc) != name {
			continue
		}
		short := strings.TrimPrefix(svc.Spec.Name, name+"_")
		out[short] = exportService(svc)
	}
	if len(out) == 0 {
		http.NotFound(w, r)
		return
	}

	buf, err := exportComposeYAML(out)
	if err != nil {
		http.Error(w, "render compose: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.yml"`)
	w.Header().Set("Content-Type", "application/x-yaml")
	_, _ = w.Write(buf)
}

func (s *Server) handleServiceExport(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	svc, err := s.getService(r.Context(), name)
	if err != nil {
		http.Error(w, "get service: "+err.Error(), http.StatusNotFound)
		return
	}
	short := svc.Spec.Name
	if stack := stackName(svc); stack != "" {
		short = strings.TrimPrefix(svc.Spec.Name, stack+"_")
	}
	buf, err := exportComposeYAML(map[string]compose.Service{short: exportService(svc)})
	if err != nil {
		http.Error(w, "render compose: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+svc.Spec.Name+`.yml"`)
	w.Header().Set("Content-Type", "application/x-yaml")
	_, _ = w.Write(buf)
}
