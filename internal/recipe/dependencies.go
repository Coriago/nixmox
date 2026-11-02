package recipe

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"text/template/parse"
)

type input struct {
	name        string
	observation bool
}

func (i input) key() string {
	if i.observation {
		return "observation:" + i.name
	}
	return "field:" + i.name
}

type dependencyNode struct {
	input
	depends []input
}

var identifier = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

func (d Definition) order() ([]dependencyNode, error) {
	graph := map[string]dependencyNode{}
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
		node := dependencyNode{input: input{name: name}, depends: dependencies}
		graph[node.key()] = node
	}
	for name, spec := range d.Observations {
		if !identifier.MatchString(name) {
			return nil, fmt.Errorf("invalid observation name %q", name)
		}
		switch spec.Type {
		case "clusterHosts", "clusterRepos", "clusterCreds", "hostDisks", "hostHardware", "hostSSHtoAgeKey", "repoCommand", "repoFile":
		default:
			return nil, fmt.Errorf("unknown observation type %q", spec.Type)
		}
		value, err := object(spec)
		if err != nil {
			return nil, err
		}
		dependencies, err := d.dependencies(value)
		if err != nil {
			return nil, err
		}
		node := dependencyNode{input: input{name: name, observation: true}, depends: dependencies}
		graph[node.key()] = node
	}
	marks := map[string]int{}
	var result []dependencyNode
	var visit func(string) error
	visit = func(key string) error {
		if marks[key] == 1 {
			return fmt.Errorf("recipe dependency cycle at %s", key)
		}
		if marks[key] == 2 {
			return nil
		}
		node, ok := graph[key]
		if !ok {
			return fmt.Errorf("undefined recipe dependency %s", key)
		}
		marks[key] = 1
		for _, dependency := range node.depends {
			if err := visit(dependency.key()); err != nil {
				return err
			}
		}
		marks[key] = 2
		result = append(result, node)
		return nil
	}
	for _, key := range keys(graph) {
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

func (d Definition) dependencies(value any) ([]input, error) {
	found := map[string]input{}
	add := func(value input) { found[value.key()] = value }
	reference := func(path []string) {
		if len(path) == 0 {
			for name := range d.Fields {
				add(input{name: name})
			}
			for name := range d.Observations {
				add(input{name: name, observation: true})
			}
			return
		}
		if path[0] == "observations" {
			if len(path) > 1 {
				add(input{name: path[1], observation: true})
			} else {
				for name := range d.Observations {
					add(input{name: name, observation: true})
				}
			}
		} else if _, ok := d.Fields[path[0]]; ok {
			add(input{name: path[0]})
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
					reference(append(append([]string(nil), field.Ident...), name.Text))
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
			t, err := parseTemplate(value)
			if err != nil {
				return err
			}
			for _, associated := range t.Templates() {
				walk(associated.Tree.Root)
			}
		case map[string]any:
			for _, key := range keys(value) {
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
	result := make([]input, 0, len(found))
	for _, dependency := range found {
		result = append(result, dependency)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].key() < result[j].key() })
	return result, nil
}

func (r Record) ready(node dependencyNode) bool {
	for _, dependency := range node.depends {
		if dependency.observation {
			if _, ok := r.Collected.Observations[dependency.name]; !ok {
				return false
			}
		} else if _, ok := r.Collected.Fields[dependency.name]; !ok {
			return false
		}
	}
	return true
}
