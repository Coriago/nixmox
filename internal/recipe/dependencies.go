package recipe

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"
	"text/template"
	"text/template/parse"
)

// Input is a field or observation and the inputs it requires.
type Input struct {
	Name        string
	Observation bool
	Depends     []Input
}

func (i Input) key() string {
	if i.Observation {
		return "observation:" + i.Name
	}
	return "field:" + i.Name
}

var identifier = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

func (d Definition) Order() ([]Input, error) {
	graph := map[string]Input{}
	for name, field := range d.Fields {
		if !identifier.MatchString(name) || name == "observations" || name == "targetRepo" || name == "recipeSource" {
			return nil, fmt.Errorf("invalid or reserved field name %q", name)
		}
		switch field.Type {
		case "text", "select", "multiselect", "boolean":
		default:
			return nil, fmt.Errorf("unknown field type %q", field.Type)
		}
		dependencies, err := d.dependencies(field.Values)
		if err != nil {
			return nil, err
		}
		node := Input{Name: name, Depends: dependencies}
		graph[node.key()] = node
	}
	for name, spec := range d.Observations {
		if !identifier.MatchString(name) {
			return nil, fmt.Errorf("invalid observation name %q", name)
		}
		if !slices.Contains([]ObservationType{
			ClusterHosts, ClusterRepos, ClusterCreds,
			HostDisks, HostHardware, HostSSHtoAgeKey,
			RepoCommand, RepoFile,
		}, spec.Type) {
			return nil, fmt.Errorf("unknown observation type %q", spec.Type)
		}
		value, err := Object(spec)
		if err != nil {
			return nil, err
		}
		dependencies, err := d.dependencies(value)
		if err != nil {
			return nil, err
		}
		node := Input{Name: name, Observation: true, Depends: dependencies}
		graph[node.key()] = node
	}

	// A visiting node encountered again forms a cycle; visited nodes are complete.
	const (
		visiting = 1
		visited  = 2
	)
	marks := map[string]int{}
	var result []Input
	var visit func(string) error
	visit = func(key string) error {
		if marks[key] == visiting {
			return fmt.Errorf("recipe dependency cycle at %s", key)
		}
		if marks[key] == visited {
			return nil
		}
		node, ok := graph[key]
		if !ok {
			return fmt.Errorf("undefined recipe dependency %s", key)
		}
		marks[key] = visiting
		for _, dependency := range node.Depends {
			if err := visit(dependency.key()); err != nil {
				return err
			}
		}
		marks[key] = visited
		result = append(result, node)
		return nil
	}
	for _, key := range slices.Sorted(maps.Keys(graph)) {
		if err := visit(key); err != nil {
			return nil, err
		}
	}
	if len(d.Outputs) > 256 {
		return nil, errors.New("recipe exceeds 256 output files")
	}
	for path, output := range d.Outputs {
		count := 0
		if output.Template != nil {
			count++
		}
		if output.Raw != nil {
			count++
		}
		if output.UpdateSOPS != nil {
			count++
		}
		if count != 1 {
			return nil, fmt.Errorf("output %q requires exactly one render method", path)
		}
	}
	return result, nil
}

func (d Definition) dependencies(value any) ([]Input, error) {
	found := map[string]Input{}
	add := func(value Input) { found[value.key()] = value }
	reference := func(path []string) {
		if len(path) == 0 {
			for name := range d.Fields {
				add(Input{Name: name})
			}
			for name := range d.Observations {
				add(Input{Name: name, Observation: true})
			}
			return
		}
		if path[0] == "observations" {
			if len(path) > 1 {
				add(Input{Name: path[1], Observation: true})
			} else {
				for name := range d.Observations {
					add(Input{Name: name, Observation: true})
				}
			}
		} else if _, ok := d.Fields[path[0]]; ok {
			add(Input{Name: path[0]})
		}
	}
	var walk func(parse.Node)
	walk = func(node parse.Node) {
		switch node := node.(type) {
		case *parse.ListNode:
			if node != nil {
				for _, child := range node.Nodes {
					walk(child)
				}
			}
		case *parse.ActionNode:
			walk(node.Pipe)
		case *parse.PipeNode:
			if node != nil {
				for _, command := range node.Cmds {
					walk(command)
				}
			}
		case *parse.CommandNode:
			// Literal index syntax has the same dependency as a field reference.
			if len(node.Args) >= 3 {
				function, ok := node.Args[0].(*parse.IdentifierNode)
				field, isField := node.Args[1].(*parse.FieldNode)
				name, isName := node.Args[2].(*parse.StringNode)
				if ok && function.Ident == "index" && isField && isName {
					path := append([]string(nil), field.Ident...)
					path = append(path, name.Text)
					reference(path)
					for _, arg := range node.Args[3:] {
						walk(arg)
					}
					return
				}
			}
			for _, arg := range node.Args {
				walk(arg)
			}
		case *parse.FieldNode:
			reference(node.Ident)
		case *parse.VariableNode:
			if len(node.Ident) > 0 && node.Ident[0] == "$" {
				reference(node.Ident[1:])
			}
		case *parse.DotNode:
			reference(nil)
		case *parse.ChainNode:
			walk(node.Node)
		case *parse.IfNode:
			walk(node.Pipe)
			walk(node.List)
			walk(node.ElseList)
		case *parse.RangeNode:
			walk(node.Pipe)
			walk(node.List)
			walk(node.ElseList)
		case *parse.WithNode:
			walk(node.Pipe)
			walk(node.List)
			walk(node.ElseList)
		case *parse.TemplateNode:
			walk(node.Pipe)
		}
	}
	var inspect func(any) error
	inspect = func(value any) error {
		switch value := value.(type) {
		case string:
			if !strings.Contains(value, "{{") {
				return nil
			}
			t, err := template.New("dependency").Option("missingkey=error").Parse(value)
			if err != nil {
				return err
			}
			for _, associated := range t.Templates() {
				walk(associated.Tree.Root)
			}
		case map[string]any:
			for _, key := range slices.Sorted(maps.Keys(value)) {
				if err := inspect(value[key]); err != nil {
					return err
				}
			}
		case []any:
			for _, item := range value {
				if err := inspect(item); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := inspect(value); err != nil {
		return nil, err
	}
	result := make([]Input, 0, len(found))
	for _, dependency := range found {
		result = append(result, dependency)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].key() < result[j].key() })
	return result, nil
}
