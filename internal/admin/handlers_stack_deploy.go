package admin

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/docker/docker/api/types/network"

	"swarmdash/internal/compose"
	"swarmdash/internal/httpx"
	"swarmdash/internal/store"
)

var stackNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// stackVersionsKept is how many compose versions are kept per stack;
// older ones are pruned on each new deploy.
const stackVersionsKept = 50

// deployForm is the state of the Stacks -> Deploy form, round-tripped
// through every preview/error re-render.
type deployForm struct {
	Name    string
	Compose string
	Vars    string // .env-style KEY=value lines for ${VAR} interpolation
	Prune   bool
}

func readDeployForm(r *http.Request) deployForm {
	return deployForm{
		Name:    strings.TrimSpace(r.FormValue("name")),
		Compose: r.FormValue("compose"),
		Vars:    r.FormValue("vars"),
		Prune:   r.FormValue("prune") == "on",
	}
}

// load validates the form's stack name and parses its compose file with
// its variables, returning the parse warnings alongside.
func (f deployForm) load() (*compose.File, []string, error) {
	if !stackNamePattern.MatchString(f.Name) {
		return nil, nil, errors.New("invalid stack name")
	}
	vars, err := compose.ParseVars(f.Vars)
	if err != nil {
		return nil, nil, err
	}
	return compose.Load([]byte(f.Compose), vars)
}

func (s *Server) renderDeployForm(w http.ResponseWriter, r *http.Request, f deployForm, extra map[string]any) {
	data := map[string]any{
		"User":    userFromContext(r),
		"Name":    f.Name,
		"Compose": f.Compose,
		"Vars":    f.Vars,
		"Prune":   f.Prune,
	}
	if f.Name != "" {
		if gs, ok := s.gitStackFor(f.Name); ok {
			data["GitOpsRepo"] = gs.RepoURL
		}
	}
	for k, v := range extra {
		data[k] = v
	}
	s.render(w, r, "stack_deploy.html", data)
}

// gitStackFor returns the GitOps definition managing stackName, if any -
// a manual edit to such a stack is overwritten by the next sync, which the
// deploy form warns about.
func (s *Server) gitStackFor(stackName string) (store.GitStack, bool) {
	stacks, err := s.store.ListGitStacks()
	if err != nil {
		return store.GitStack{}, false
	}
	for _, gs := range stacks {
		if gs.StackName == stackName {
			return gs, true
		}
	}
	return store.GitStack{}, false
}

// handleStackDeployPage serves the deploy form, pre-filled from one of:
// ?template=<id> (built-in or saved template), ?stack=<name> ("Edit
// stack": its latest stored compose file, or an export of its running
// services if none was ever stored), or ?stack=<name>&version=<id> (a
// specific stored version).
func (s *Server) handleStackDeployPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var f deployForm
	extra := map[string]any{}

	switch {
	case q.Get("template") != "":
		if tpl, ok := s.findTemplate(q.Get("template")); ok {
			f.Name, f.Compose = stackNameSuggestion(tpl.Name), tpl.Compose
		}
	case q.Get("stack") != "":
		f.Name = q.Get("stack")
		extra["Editing"] = true
		v, err := s.stackVersionFor(f.Name, q.Get("version"))
		switch {
		case err == nil:
			f.Compose = v.Compose
			if len(v.VarsEnc) > 0 {
				vars, err := s.decryptSecret(v.VarsEnc)
				if err != nil {
					extra["Error"] = "could not decrypt this version's variables with the current cluster secret - re-enter them"
				}
				f.Vars = vars
			}
			extra["LoadedVersion"] = v.Version
		case errors.Is(err, store.ErrNotFound) && q.Get("version") == "":
			yml, err := s.exportStackYAML(r, f.Name)
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			f.Compose = string(yml)
			extra["Notice"] = "No compose file was stored for this stack (it was deployed before swarmdash kept stack history, or by other means) - this one was generated from its running services. Review it before deploying."
		default:
			http.Error(w, "stack version not found", http.StatusNotFound)
			return
		}
	}
	s.renderDeployForm(w, r, f, extra)
}

// stackNameSuggestion turns a template name ("My App 2") into a default
// stack name ("my-app-2").
func stackNameSuggestion(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '.':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-.")
}

// stackVersionFor returns the given version of a stack (by ID), or its
// latest one when versionID is empty.
func (s *Server) stackVersionFor(stack, versionID string) (store.StackVersion, error) {
	if versionID != "" {
		v, err := s.store.GetStackVersion(versionID)
		if err != nil {
			return v, err
		}
		if v.StackName != stack {
			return store.StackVersion{}, store.ErrNotFound
		}
		return v, nil
	}
	versions, err := s.store.ListStackVersions(stack)
	if err != nil {
		return store.StackVersion{}, err
	}
	if len(versions) == 0 {
		return store.StackVersion{}, store.ErrNotFound
	}
	return versions[0], nil
}

func (s *Server) handleStackDeployPreview(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	f := readDeployForm(r)
	file, warnings, err := f.load()
	if err != nil {
		s.renderDeployForm(w, r, f, map[string]any{"Error": err.Error()})
		return
	}
	diffs, err := s.previewCompose(r.Context(), f.Name, file)
	if err != nil {
		s.renderDeployForm(w, r, f, map[string]any{"Error": err.Error(), "Warnings": warnings})
		return
	}
	s.renderDeployForm(w, r, f, map[string]any{"Diffs": diffs, "Warnings": warnings})
}

func (s *Server) handleStackDeploySubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	f := readDeployForm(r)
	file, warnings, err := f.load()
	if err != nil {
		s.renderDeployForm(w, r, f, map[string]any{"Error": err.Error()})
		return
	}

	result, err := s.applyCompose(r.Context(), f.Name, file, applyOptions{Prune: f.Prune})
	if err != nil {
		s.audit(r, "stack.deploy", f.Name, "", err)
		s.renderDeployForm(w, r, f, map[string]any{"Error": err.Error(), "Warnings": warnings})
		return
	}

	s.recordStackVersion(f.Name, f.Compose, f.Vars, "ui", "", usernameOf(r))
	s.audit(r, "stack.deploy", f.Name, fmt.Sprintf("created=%d updated=%d removed=%d", len(result.Created), len(result.Updated), len(result.Removed)), nil)
	redirect(w, r, "/stacks/"+f.Name)
}

// handleStackRollback redeploys a stored compose version as-is (with the
// variables it was deployed with), removing services that version doesn't
// define, and records it as a new version so the history stays linear.
func (s *Server) handleStackRollback(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	v, err := s.stackVersionFor(name, r.PathValue("id"))
	if err != nil {
		http.Error(w, "stack version not found", http.StatusNotFound)
		return
	}
	varsText := ""
	if len(v.VarsEnc) > 0 {
		if varsText, err = s.decryptSecret(v.VarsEnc); err != nil {
			http.Error(w, "could not decrypt this version's variables with the current cluster secret - open it in the editor and re-enter them", http.StatusConflict)
			return
		}
	}
	f := deployForm{Name: name, Compose: v.Compose, Vars: varsText, Prune: true}
	file, _, err := f.load()
	if err != nil {
		http.Error(w, "stored compose file no longer parses: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if _, err := s.applyCompose(r.Context(), name, file, applyOptions{Prune: true}); err != nil {
		s.audit(r, "stack.rollback", name, fmt.Sprintf("version=%d", v.Version), err)
		http.Error(w, "roll back: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.recordStackVersion(name, v.Compose, varsText, "rollback", fmt.Sprintf("rollback to v%d", v.Version), usernameOf(r))
	s.audit(r, "stack.rollback", name, fmt.Sprintf("version=%d", v.Version), nil)
	redirect(w, r, "/stacks/"+name)
}

// recordStackVersion stores a successfully deployed compose file as the
// stack's next version - unless it's identical (file and variables) to
// the latest one, so re-deploying or a no-op GitOps sync doesn't pad the
// history. Failures are logged, not returned: the deploy itself already
// happened, and history is a convenience on top of it.
func (s *Server) recordStackVersion(stack, composeText, varsText, source, note, username string) {
	versions, err := s.store.ListStackVersions(stack)
	if err != nil {
		s.log.Error("stack history: list versions", "stack", stack, "err", err)
		return
	}
	next := 1
	if len(versions) > 0 {
		latest := versions[0]
		next = latest.Version + 1
		latestVars := ""
		if len(latest.VarsEnc) > 0 {
			latestVars, _ = s.decryptSecret(latest.VarsEnc)
		}
		if latest.Compose == composeText && latestVars == varsText {
			return
		}
	}

	v := store.StackVersion{
		ID:        randomToken(8),
		StackName: stack,
		Version:   next,
		Compose:   composeText,
		Source:    source,
		Note:      note,
		CreatedBy: username,
		CreatedAt: time.Now(),
	}
	if strings.TrimSpace(varsText) != "" {
		if v.VarsEnc, err = s.encryptSecret(varsText); err != nil {
			s.log.Error("stack history: encrypt variables", "stack", stack, "err", err)
			return
		}
	}
	if err := s.store.PutStackVersion(v); err != nil {
		s.log.Error("stack history: save version", "stack", stack, "err", err)
		return
	}
	if err := s.store.PruneStackVersions(stack, stackVersionsKept); err != nil {
		s.log.Error("stack history: prune", "stack", stack, "err", err)
	}
}

func usernameOf(r *http.Request) string {
	if u := userFromContext(r); u != nil {
		return u.Username
	}
	return ""
}

func (s *Server) handleTemplatesPage(w http.ResponseWriter, r *http.Request) {
	custom, err := s.store.ListStackTemplates()
	if err != nil {
		http.Error(w, "list templates: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, r, "templates_gallery.html", map[string]any{
		"User":      userFromContext(r),
		"Templates": builtinTemplates,
		"Custom":    custom,
	})
}

// handleTemplateSave saves the deploy form's compose file as a custom
// template. Saving under the name of an existing custom template replaces
// it, so a template can be edited by loading it, changing it and saving it
// again under the same name.
func (s *Server) handleTemplateSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	f := readDeployForm(r)
	tplName := strings.TrimSpace(r.FormValue("template_name"))
	if tplName == "" {
		s.renderDeployForm(w, r, f, map[string]any{"Error": "template name is required"})
		return
	}
	if _, err := compose.Parse([]byte(f.Compose)); err != nil {
		s.renderDeployForm(w, r, f, map[string]any{"Error": err.Error()})
		return
	}

	tpl := store.StackTemplate{
		ID:          randomToken(8),
		Name:        tplName,
		Description: strings.TrimSpace(r.FormValue("template_description")),
		Compose:     f.Compose,
		CreatedBy:   usernameOf(r),
		CreatedAt:   time.Now(),
	}
	existing, err := s.store.ListStackTemplates()
	if err != nil {
		http.Error(w, "list templates: "+err.Error(), http.StatusInternalServerError)
		return
	}
	for _, t := range existing {
		if strings.EqualFold(t.Name, tplName) {
			tpl.ID = t.ID
		}
	}
	if err := s.store.PutStackTemplate(tpl); err != nil {
		http.Error(w, "save template: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.audit(r, "template.save", tplName, "", nil)
	redirect(w, r, "/stacks/templates")
}

func (s *Server) handleTemplateDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tpl, err := s.store.GetStackTemplate(id)
	if err != nil {
		http.Error(w, "template not found", http.StatusNotFound)
		return
	}
	if err := s.store.DeleteStackTemplate(id); err != nil {
		http.Error(w, "delete template: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.audit(r, "template.delete", tpl.Name, "", nil)
	redirect(w, r, "/stacks/templates")
}

// exportStackYAML renders the running services of a stack as a compose
// file that can be deployed again as-is.
func (s *Server) exportStackYAML(r *http.Request, name string) ([]byte, error) {
	services, err := s.listServices(r.Context())
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	nets, _ := s.docker.NetworkList(r.Context(), network.ListOptions{})
	x := newComposeExporter(name, nets)
	for _, svc := range services {
		if stackName(svc) == name {
			x.add(svc)
		}
	}
	if len(x.file.Services) == 0 {
		return nil, fmt.Errorf("stack %q not found", name)
	}
	return exportComposeYAML(x.file)
}

func (s *Server) handleStackExport(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	buf, err := s.exportStackYAML(r, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	httpx.SetAttachment(w, name+".yml")
	w.Header().Set("Content-Type", "application/x-yaml")
	_, _ = w.Write(buf)
}

// handleStackVersionDownload serves a stored compose version as a file.
// Only the compose text - never the (encrypted) variables.
func (s *Server) handleStackVersionDownload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	v, err := s.stackVersionFor(name, r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	httpx.SetAttachment(w, fmt.Sprintf("%s-v%d.yml", name, v.Version))
	w.Header().Set("Content-Type", "application/x-yaml")
	_, _ = w.Write([]byte(v.Compose))
}

func (s *Server) handleServiceExport(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	svc, err := s.getService(r.Context(), name)
	if err != nil {
		http.Error(w, "get service: "+err.Error(), http.StatusNotFound)
		return
	}
	nets, _ := s.docker.NetworkList(r.Context(), network.ListOptions{})
	x := newComposeExporter(stackName(svc), nets)
	x.add(svc)
	buf, err := exportComposeYAML(x.file)
	if err != nil {
		http.Error(w, "render compose: "+err.Error(), http.StatusInternalServerError)
		return
	}
	httpx.SetAttachment(w, svc.Spec.Name+".yml")
	w.Header().Set("Content-Type", "application/x-yaml")
	_, _ = w.Write(buf)
}
