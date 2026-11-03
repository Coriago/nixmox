package recipe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.opentelemetry.io/otel"

	"nixmox/internal/source"
)

type Rendered struct {
	Files              []string          `json:"files"`
	Contents           map[string]string `json:"contents"`
	FlakeDir           string            `json:"flakeDir"`
	FlakeConfiguration string            `json:"flakeConfiguration"`
}

// Render writes only inside the supplied fresh pinned worktree. Callers discard
// the worktree on any error; templates and paths are validated before writing.
func Render(ctx context.Context, definition Definition, inputs map[string]any, work *source.Worktree) (Rendered, error) {
	ctx, span := otel.Tracer("nixmox.recipe").Start(ctx, "recipe.render")
	defer span.End()
	var result Rendered
	root, err := os.OpenRoot(work.Path)
	if err != nil {
		return result, err
	}
	defer root.Close()
	result.FlakeDir, err = Text(ctx, definition.FlakeDir, inputs)
	if err != nil {
		return result, err
	}
	if result.FlakeDir == "" {
		result.FlakeDir = "."
	}
	if !filepath.IsLocal(result.FlakeDir) {
		return result, errors.New("flake directory must stay inside worktree")
	}
	result.FlakeConfiguration, err = Text(ctx, definition.FlakeConfiguration, inputs)
	if err != nil {
		return result, err
	}
	if strings.TrimSpace(result.FlakeConfiguration) == "" {
		return result, errors.New("flake configuration required")
	}
	files := map[string][]byte{}
	total := 0
	for _, pattern := range slices.Sorted(maps.Keys(definition.Outputs)) {
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
		content, err := renderOutput(ctx, definition.Outputs[pattern], inputs, root, path)
		if err != nil {
			return result, err
		}
		total += len(content)
		if total > RenderLimit {
			return result, errors.New("rendered files exceed 1 MiB total")
		}
		files[path] = content
	}
	result.Contents = map[string]string{}
	for _, path := range slices.Sorted(maps.Keys(files)) {
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
		result.Contents[path] = string(files[path])
	}
	return result, nil
}

// renderOutput selects a method; each method owns only its content generation.
func renderOutput(ctx context.Context, output Output, inputs map[string]any, root *os.Root, path string) ([]byte, error) {
	switch {
	case output.Template != nil:
		return renderTemplateOutput(ctx, *output.Template, inputs)
	case output.Raw != nil:
		return renderRawOutput(ctx, *output.Raw, inputs)
	case output.UpdateSOPS != nil:
		return renderSOPSOutput(ctx, *output.UpdateSOPS, inputs, root, path)
	default:
		return nil, errors.New("output requires a render method")
	}
}

func renderTemplateOutput(ctx context.Context, text string, inputs map[string]any) ([]byte, error) {
	parsed, err := parseTemplate(text)
	if err != nil {
		return nil, err
	}
	// A whole-value expression can select template source from an observation.
	if singleAction(parsed) != nil {
		value, err := Evaluate(ctx, text, inputs)
		if err != nil {
			return nil, err
		}
		var ok bool
		text, ok = value.(string)
		if !ok {
			return nil, errors.New("output template source must resolve to text")
		}
	}
	rendered, err := Text(ctx, text, inputs)
	if err != nil {
		return nil, err
	}
	return []byte(rendered), nil
}

func renderRawOutput(ctx context.Context, expression string, inputs map[string]any) ([]byte, error) {
	value, err := Evaluate(ctx, expression, inputs)
	if err != nil {
		return nil, err
	}
	text, ok := value.(string)
	if !ok {
		return nil, errors.New("raw output must resolve to text")
	}
	return []byte(text), nil
}

func renderSOPSOutput(ctx context.Context, change SOPSUpdate, inputs map[string]any, root *os.Root, path string) ([]byte, error) {
	var err error
	change.Key, err = Text(ctx, change.Key, inputs)
	if err != nil {
		return nil, err
	}
	change.AnchorValue, err = Text(ctx, change.AnchorValue, inputs)
	if err != nil {
		return nil, err
	}
	change.Rules = slices.Clone(change.Rules)
	for i, rule := range change.Rules {
		change.Rules[i], err = Text(ctx, rule, inputs)
		if err != nil {
			return nil, err
		}
	}
	existing, err := readOutput(root, path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return UpdateSOPS(existing, change)
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
