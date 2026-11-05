package scaffold

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lens077/go-connect-template-cli/internal/manifest"
)

// PruneOptions selects an existing service and an empty destination for its
// template snapshot. The service is read only; no business resources are copied.
type PruneOptions struct {
	ReferenceDir string
	OutputDir    string
	Name         string
	Render       RenderParams
}

// PrunedService describes the template skeleton produced for a reference service.
type PrunedService struct {
	ReferenceRoot string
	ServiceRoot   string
	Options       Options
	Features      []string
}

// PruneServiceTemplate applies the template's manifest using the reference
// service's identity, layout, and detected features. It is shared by upgrade
// and template prune, so both commands use the same source snapshot.
func PruneServiceTemplate(ctx context.Context, src Source, m *manifest.Manifest, opts PruneOptions) (*PrunedService, error) {
	if opts.ReferenceDir == "" || opts.OutputDir == "" {
		return nil, fmt.Errorf("reference service and output directories are required")
	}
	info, err := InspectTarget(opts.ReferenceDir)
	if err != nil {
		return nil, err
	}
	layoutName, err := detectLayout(info, m)
	if err != nil {
		return nil, err
	}
	layout := m.Layouts[layoutName]
	name := opts.Name
	if name == "" {
		name = detectName(info, layout)
	}
	if err := checkServiceDir(info.Root, layout, name); err != nil {
		return nil, err
	}

	output, err := filepath.Abs(opts.OutputDir)
	if err != nil {
		return nil, err
	}
	for _, input := range []string{src.Root, info.Root} {
		input, err = filepath.Abs(input)
		if err != nil {
			return nil, err
		}
		if pathsOverlap(output, input) {
			return nil, fmt.Errorf("output directory %s must not overlap input %s", output, input)
		}
	}

	features, err := DetectFeatures(info.Root, m)
	if err != nil {
		return nil, fmt.Errorf("detect features: %w", err)
	}
	plan, err := NewPlan(src, m, Options{
		Name:            name,
		Module:          info.Module,
		Dest:            output,
		Layout:          layoutName,
		Features:        enabledFeatures(features),
		NoResource:      true,
		KeepExample:     detectKeepExample(info.Root, m),
		ServiceName:     opts.Render.ServiceName,
		DockerRegistry:  opts.Render.DockerRegistry,
		DockerNamespace: opts.Render.DockerNamespace,
		ConsulAddr:      opts.Render.ConsulAddr,
	})
	if err != nil {
		return nil, err
	}
	if err := ensureEmpty(output); err != nil {
		return nil, err
	}

	// A comparison snapshot must not run generators, contact infrastructure,
	// or change the target service's dependency graph.
	plan.Hooks = nil
	if err := Apply(ctx, plan, DiscardReporter{}); err != nil {
		return nil, fmt.Errorf("write pruned template: %w", err)
	}
	var resolved []string
	for _, feature := range plan.Features.Names() {
		if _, ok := m.Features[feature]; ok {
			resolved = append(resolved, feature)
		}
	}
	return &PrunedService{
		ReferenceRoot: info.Root,
		ServiceRoot:   filepath.Join(plan.Dest, plan.ServiceDir),
		Options:       plan.Opts,
		Features:      resolved,
	}, nil
}

func pathsOverlap(a, b string) bool {
	within := func(parent, child string) bool {
		rel, err := filepath.Rel(parent, child)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	return within(a, b) || within(b, a)
}
