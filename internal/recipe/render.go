package recipe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"go.opentelemetry.io/otel"
	"nixmox/internal/source"
)

type Rendered struct {
	Files              []string `json:"files"`
	FlakeDir           string   `json:"flakeDir"`
	FlakeConfiguration string   `json:"flakeConfiguration"`
}

// Render writes only inside the supplied fresh pinned worktree. Callers discard
// the worktree on any error; templates and paths are validated before writing.
func Render(ctx context.Context, record Record, work *source.Worktree) (Rendered, error) {
	ctx, span := otel.Tracer("nixmox.recipe").Start(ctx, "recipe.render")
	defer span.End()
	var result Rendered
	current, err := clone(record)
	if err != nil {
		return result, err
	}
	if err = current.Normalize(ctx); err != nil {
		return result, err
	}
	if !current.CanRender {
		return result, errors.New("recipe has missing or stale inputs")
	}
	if current.TargetRepo != work.Pin {
		return result, errors.New("worktree does not match recipe target pin")
	}
	root, err := os.OpenRoot(work.Path)
	if err != nil {
		return result, err
	}
	defer root.Close()
	inputs, err := current.inputs()
	if err != nil {
		return result, err
	}
	result.FlakeDir, err = Text(ctx, current.SourceDefinition.FlakeDir, inputs)
	if err != nil {
		return result, err
	}
	if result.FlakeDir == "" {
		result.FlakeDir = "."
	}
	if !filepath.IsLocal(result.FlakeDir) {
		return result, errors.New("flake directory must stay inside worktree")
	}
	result.FlakeConfiguration, err = Text(ctx, current.SourceDefinition.FlakeConfiguration, inputs)
	if err != nil {
		return result, err
	}
	if strings.TrimSpace(result.FlakeConfiguration) == "" {
		return result, errors.New("flake configuration required")
	}
	files := map[string][]byte{}
	for _, pattern := range keys(current.SourceDefinition.Outputs) {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		path, err := Text(ctx, pattern, inputs)
		if err != nil {
			return result, err
		}
		if err = safeOutput(root, path); err != nil {
			return result, err
		}
		path = filepath.Clean(path)
		if _, exists := files[path]; exists {
			return result, fmt.Errorf("duplicate rendered output %q", path)
		}
		output := current.SourceDefinition.Outputs[pattern]
		var content []byte
		switch {
		case output.Template != nil:
			text := *output.Template
			parsed, err := parseTemplate(text)
			if err != nil {
				return result, err
			}
			if singleAction(parsed) != nil {
				value, err := Evaluate(ctx, text, inputs)
				if err != nil {
					return result, err
				}
				var ok bool
				text, ok = value.(string)
				if !ok {
					return result, errors.New("output template source must resolve to text")
				}
			}
			rendered, err := Text(ctx, text, inputs)
			if err != nil {
				return result, err
			}
			content = []byte(rendered)
		case output.Raw != nil:
			value, err := Evaluate(ctx, *output.Raw, inputs)
			if err != nil {
				return result, err
			}
			text, ok := value.(string)
			if !ok {
				return result, errors.New("raw output must resolve to text")
			}
			content = []byte(text)
		case output.UpdateSOPS != nil:
			change := *output.UpdateSOPS
			change.Key, err = Text(ctx, change.Key, inputs)
			if err != nil {
				return result, err
			}
			change.AnchorValue, err = Text(ctx, change.AnchorValue, inputs)
			if err != nil {
				return result, err
			}
			change.Rules = append([]string(nil), change.Rules...)
			for i, rule := range change.Rules {
				change.Rules[i], err = Text(ctx, rule, inputs)
				if err != nil {
					return result, err
				}
			}
			existing, err := readOutput(root, path)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return result, err
			}
			content, err = UpdateSOPS(existing, change)
			if err != nil {
				return result, err
			}
		}
		if len(content) > RenderLimit {
			return result, errors.New("output exceeds 1 MiB")
		}
		files[path] = content
	}
	for _, path := range keys(files) {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		if err = root.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return result, err
		}
		file, err := root.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
		if err != nil {
			return result, err
		}
		_, writeErr := file.Write(files[path])
		closeErr := file.Close()
		if err = errors.Join(writeErr, closeErr); err != nil {
			return result, err
		}
		result.Files = append(result.Files, path)
	}
	return result, nil
}

func safeOutput(root *os.Root, path string) error {
	if !filepath.IsLocal(path) || filepath.Clean(path) == "." {
		return errors.New("output path must name a file inside worktree")
	}
	current := ""
	for _, component := range strings.Split(filepath.Clean(path), string(filepath.Separator)) {
		if strings.EqualFold(component, ".git") {
			return errors.New("recipe cannot modify Git metadata")
		}
		current = filepath.Join(current, component)
		info, err := root.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("recipe output cannot traverse symlinks")
		}
	}
	return nil
}
func readOutput(root *os.Root, path string) ([]byte, error) {
	file, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, RenderLimit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > RenderLimit {
		return nil, errors.New("existing output exceeds 1 MiB")
	}
	return data, nil
}
