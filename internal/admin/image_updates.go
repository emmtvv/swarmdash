package admin

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/docker/docker/api/types/swarm"

	"swarmdash/internal/imageupdate"
	"swarmdash/internal/store"
)

const (
	// autoUpdateLabel is the service label that sets a service's own
	// automatic image update policy ("off", "patch", "minor" or "major"),
	// overriding the cluster-wide default from Settings -> General. It can be
	// set from the service page or declared in compose under deploy.labels,
	// which is what keeps it across stack redeploys.
	autoUpdateLabel = "swarmdash.autoupdate"

	imageUpdateInterval = time.Hour
	// imageUpdateFirstRun delays the first automatic check after startup,
	// so a restarting admin doesn't immediately roll out updates while the
	// operator may still be looking into why it restarted.
	imageUpdateFirstRun = 5 * time.Minute

	tagCacheTTL      = 10 * time.Minute
	tagCacheErrorTTL = time.Minute
)

// tagCache keeps each repository's tag list for tagCacheTTL, so the
// service page and the hourly updater don't ask the registry again for
// every service running the same image. The zero value is ready to use.
type tagCache struct {
	mu      sync.Mutex
	entries map[string]tagCacheEntry
}

type tagCacheEntry struct {
	tags    []string
	err     error
	expires time.Time
}

func (s *Server) imageRegistry() *imageupdate.Client {
	if s.registryClient != nil {
		return s.registryClient
	}
	return imageupdate.DefaultClient
}

// imageTags lists ref's repository tags, from the cache when it's fresh.
func (s *Server) imageTags(ctx context.Context, ref imageupdate.Ref) ([]string, error) {
	key := ref.Domain + "/" + ref.Path
	now := time.Now()
	s.tagCache.mu.Lock()
	e, ok := s.tagCache.entries[key]
	s.tagCache.mu.Unlock()
	if ok && now.Before(e.expires) {
		return e.tags, e.err
	}

	tags, err := s.imageRegistry().ListTags(ctx, ref, s.registryCredential(ref.Repository))
	e = tagCacheEntry{tags: tags, err: err, expires: now.Add(tagCacheTTL)}
	if err != nil {
		e.expires = now.Add(tagCacheErrorTTL)
	}
	s.tagCache.mu.Lock()
	if s.tagCache.entries == nil {
		s.tagCache.entries = map[string]tagCacheEntry{}
	}
	s.tagCache.entries[key] = e
	s.tagCache.mu.Unlock()
	return tags, err
}

// registryCredential returns the stored login for image's registry, or an
// anonymous credential - same lookup as encodedRegistryAuthFor.
func (s *Server) registryCredential(image string) imageupdate.Credential {
	cred, password, ok, err := s.storedRegistryLogin(image)
	if err != nil {
		s.log.Warn("decrypt registry credential", "server", cred.Server, "err", err)
	}
	if !ok {
		return imageupdate.Credential{}
	}
	return imageupdate.Credential{Username: cred.Username, Password: password}
}

// imageUpdates is what the service page shows about newer versions of the
// service's image.
type imageUpdates struct {
	ServiceName string
	Ref         imageupdate.Ref
	IsVersion   bool
	Patch       string // newest tag within the current major.minor, "" if none
	Minor       string // newest tag within the current major, "" if none
	Major       string // newest tag overall, "" if none
	Err         string

	Policy        string // the service's autoUpdateLabel, "" when unset
	DefaultPolicy imageupdate.Policy
}

func (u imageUpdates) PatchImage() string { return u.Ref.WithTag(u.Patch) }
func (u imageUpdates) MinorImage() string { return u.Ref.WithTag(u.Minor) }
func (u imageUpdates) MajorImage() string { return u.Ref.WithTag(u.Major) }

// EffectivePolicy is the policy the automatic updater applies.
func (u imageUpdates) EffectivePolicy() imageupdate.Policy {
	if p, ok := imageupdate.ParsePolicy(u.Policy); ok {
		return p
	}
	return u.DefaultPolicy
}

// defaultImagePolicy reads the cluster-wide policy from Settings -> General.
func (s *Server) defaultImagePolicy() imageupdate.Policy {
	cfg, err := s.store.GetAppSettings()
	if err != nil {
		return imageupdate.PolicyOff
	}
	if p, ok := imageupdate.ParsePolicy(cfg.ImageAutoUpdate); ok {
		return p
	}
	return imageupdate.PolicyOff
}

// handleServiceImageUpdates renders the "Newer versions" fragment of the
// service page. It's fetched after the page loads, so a slow or
// unreachable registry doesn't hold up the rest of the page.
func (s *Server) handleServiceImageUpdates(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	svc, err := s.getService(ctx, r.PathValue("name"))
	if err != nil {
		http.Error(w, "get service: "+err.Error(), http.StatusNotFound)
		return
	}
	u := imageUpdates{
		ServiceName:   svc.Spec.Name,
		Policy:        svc.Spec.Labels[autoUpdateLabel],
		DefaultPolicy: s.defaultImagePolicy(),
	}
	ref, err := imageupdate.ParseRef(svc.Spec.TaskTemplate.ContainerSpec.Image)
	if err != nil {
		u.Err = err.Error()
	} else {
		u.Ref = ref
		u.IsVersion = imageupdate.IsVersionTag(ref.Tag)
		if u.IsVersion {
			reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			if tags, err := s.imageTags(reqCtx, ref); err != nil {
				u.Err = err.Error()
			} else {
				u.Patch = imageupdate.Newest(ref.Tag, tags, imageupdate.PolicyPatch)
				u.Minor = imageupdate.Newest(ref.Tag, tags, imageupdate.PolicyMinor)
				u.Major = imageupdate.Newest(ref.Tag, tags, imageupdate.PolicyMajor)
			}
		}
	}
	s.renderer.RenderFragment(w, "partial_image_updates.html", u, s.requestLanguage(r))
}

// handleServiceAutoUpdate sets or clears the service's autoUpdateLabel. It
// only touches service labels, so no task is restarted.
func (s *Server) handleServiceAutoUpdate(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	policy := r.FormValue("policy")
	if _, ok := imageupdate.ParsePolicy(policy); !ok && policy != "" {
		http.Error(w, "invalid policy", http.StatusBadRequest)
		return
	}

	svc, err := s.getService(ctx, name)
	if err != nil {
		http.Error(w, "get service: "+err.Error(), http.StatusNotFound)
		return
	}
	if policy == "" {
		delete(svc.Spec.Labels, autoUpdateLabel)
	} else {
		if svc.Spec.Labels == nil {
			svc.Spec.Labels = map[string]string{}
		}
		svc.Spec.Labels[autoUpdateLabel] = policy
	}
	if _, err := s.docker.ServiceUpdate(ctx, svc.ID, svc.Version, svc.Spec, swarm.ServiceUpdateOptions{}); err != nil {
		http.Error(w, "update service: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.audit(r, "service.autoupdate", svc.Spec.Name, "policy="+policy, nil)
	redirect(w, r, "/services/"+name)
}

// runImageUpdater applies automatic image updates: every
// imageUpdateInterval, each service whose policy (its autoUpdateLabel, or
// the cluster default) isn't off is moved to the newest tag that policy
// allows.
func (s *Server) runImageUpdater(ctx context.Context) {
	timer := time.NewTimer(imageUpdateFirstRun)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			s.imageUpdateOnce(ctx)
			timer.Reset(imageUpdateInterval)
		}
	}
}

func (s *Server) imageUpdateOnce(ctx context.Context) {
	services, err := s.listServices(ctx)
	if err != nil {
		s.log.Warn("image updater: list services", "err", err)
		return
	}
	def := s.defaultImagePolicy()
	for _, svc := range services {
		policy := def
		if p, ok := imageupdate.ParsePolicy(svc.Spec.Labels[autoUpdateLabel]); ok {
			policy = p
		}
		if policy == imageupdate.PolicyOff {
			continue
		}
		if err := s.autoUpdateService(ctx, svc, policy); err != nil {
			s.log.Warn("image updater", "service", svc.Spec.Name, "err", err)
		}
	}
}

func (s *Server) autoUpdateService(ctx context.Context, svc swarm.Service, policy imageupdate.Policy) error {
	ref, err := imageupdate.ParseRef(svc.Spec.TaskTemplate.ContainerSpec.Image)
	if err != nil || !imageupdate.IsVersionTag(ref.Tag) {
		return nil
	}
	if svc.UpdateStatus != nil {
		switch svc.UpdateStatus.State {
		case swarm.UpdateStateUpdating, swarm.UpdateStatePaused,
			swarm.UpdateStateRollbackStarted, swarm.UpdateStateRollbackPaused:
			return nil // a rollout is in progress or needs attention
		}
	}
	tags, err := s.imageTags(ctx, ref)
	if err != nil {
		return err
	}
	target := imageupdate.Newest(ref.Tag, tags, policy)
	if target == "" {
		return nil
	}
	// Swarm rolled back from exactly this version (the failed spec is kept
	// as PreviousSpec) - don't retry it every hour. A newer tag than the
	// one that failed is still picked up.
	if svc.UpdateStatus != nil && svc.UpdateStatus.State == swarm.UpdateStateRollbackCompleted && svc.PreviousSpec != nil {
		if prev, err := imageupdate.ParseRef(svc.PreviousSpec.TaskTemplate.ContainerSpec.Image); err == nil && prev.Tag == target {
			return nil
		}
	}

	from, to := ref.WithTag(ref.Tag), ref.WithTag(target)
	full, err := s.getService(ctx, svc.ID)
	if err != nil {
		return err
	}
	if err := s.setServiceImage(ctx, full, to); err != nil {
		_ = s.store.AppendAudit(store.AuditEntry{
			Username: "system", Action: "service_image_updated", Target: svc.Spec.Name,
			Detail: from + " -> " + to, Error: err.Error(),
		})
		return err
	}
	s.fireWebhooks(ctx, "service_image_updated", svc.Spec.Name, fmt.Sprintf("%s -> %s (auto, %s)", from, to, policy))
	return nil
}
