package yqlib

import (
	"bytes"
	"strconv"
	"strings"

	logging "gopkg.in/op/go-logging.v1"
	yaml "gopkg.in/yaml.v3"
)

type DataNavigator interface {
	DebugNode(node *yaml.Node)
	Get(rootNode *yaml.Node, path []string) (*yaml.Node, error)
	Update(rootNode *yaml.Node, path []string, changesToApply *yaml.Node) error
	Delete(rootNode *yaml.Node, path []string) error
	GuessKind(tail []string, guess yaml.Kind) yaml.Kind
}

type navigator struct {
	log *logging.Logger
}

type VisitorFn func(*yaml.Node) error

func NewDataNavigator(l *logging.Logger) DataNavigator {
	return &navigator{
		log: l,
	}
}

func (n *navigator) Get(value *yaml.Node, path []string) (*yaml.Node, error) {
	matchingNodes := make([]*yaml.Node, 0)

	n.Visit(value, path, func(matchedNode *yaml.Node) error {
		matchingNodes = append(matchingNodes, matchedNode)
		n.log.Debug("Matched")
		n.DebugNode(matchedNode)
		return nil
	})
	n.log.Debug("finished iterating, found %v matches", len(matchingNodes))
	if len(matchingNodes) == 0 {
		return nil, nil
	} else if len(matchingNodes) == 1 {
		return matchingNodes[0], nil
	}
	// make a new node
	var newNode = yaml.Node{Kind: yaml.SequenceNode}
	newNode.Content = matchingNodes
	return &newNode, nil
}

func (n *navigator) Update(rootNode *yaml.Node, path []string, changesToApply *yaml.Node) error {
	errorVisiting := n.Visit(rootNode, path, func(nodeToUpdate *yaml.Node) error {
		n.log.Debug("going to update")
		n.DebugNode(nodeToUpdate)
		n.log.Debug("with")
		n.DebugNode(changesToApply)
		nodeToUpdate.Value = changesToApply.Value
		nodeToUpdate.Tag = changesToApply.Tag
		nodeToUpdate.Kind = changesToApply.Kind
		nodeToUpdate.Style = changesToApply.Style
		nodeToUpdate.Content = changesToApply.Content
		nodeToUpdate.HeadComment = changesToApply.HeadComment
		nodeToUpdate.LineComment = changesToApply.LineComment
		nodeToUpdate.FootComment = changesToApply.FootComment
		return nil
	})
	return errorVisiting
}

// TODO: refactor delete..
func (n *navigator) Delete(rootNode *yaml.Node, path []string) error {

	lastBit, newTail := path[len(path)-1], path[:len(path)-1]
	n.log.Debug("splitting path, %v", lastBit)
	n.log.Debug("new tail, %v", newTail)
	errorVisiting := n.Visit(rootNode, newTail, func(nodeToUpdate *yaml.Node) error {
		n.log.Debug("need to find %v in here", lastBit)
		n.DebugNode(nodeToUpdate)
		original := nodeToUpdate.Content
		if nodeToUpdate.Kind == yaml.SequenceNode {
			var index, err = strconv.ParseInt(lastBit, 10, 64) // nolint
			if err != nil {
				return err
			}
			if index >= int64(len(nodeToUpdate.Content)) {
				n.log.Debug("index %v is greater than content length %v", index, len(nodeToUpdate.Content))
				return nil
			}
			nodeToUpdate.Content = append(original[:index], original[index+1:]...)

		} else if nodeToUpdate.Kind == yaml.MappingNode {
			// need to delete in reverse - otherwise the matching indexes
			// become incorrect.
			matchingIndices := make([]int, 0)
			_, errorVisiting := n.visitMatchingEntries(nodeToUpdate.Content, lastBit, func(indexInMap int) error {
				matchingIndices = append(matchingIndices, indexInMap)
				n.log.Debug("matchingIndices %v", indexInMap)
				return nil
			})
			n.log.Debug("delete matching indices now")
			n.log.Debug("%v", matchingIndices)
			if errorVisiting != nil {
				return errorVisiting
			}
			for i := len(matchingIndices) - 1; i >= 0; i-- {
				indexToDelete := matchingIndices[i]
				n.log.Debug("deleting index %v, %v", indexToDelete, nodeToUpdate.Content[indexToDelete].Value)
				nodeToUpdate.Content = append(nodeToUpdate.Content[:indexToDelete], nodeToUpdate.Content[indexToDelete+2:]...)
			}

		}

		return nil
	})
	return errorVisiting
}

func (n *navigator) Visit(value *yaml.Node, path []string, visitor VisitorFn) error {
	realValue := value
	if realValue.Kind == yaml.DocumentNode {
		n.log.Debugf("its a document! returning the first child")
		realValue = value.Content[0]
	}
	if len(path) > 0 {
		n.log.Debugf("diving into %v", path[0])
		n.DebugNode(value)
		return n.recurse(realValue, path[0], path[1:], visitor)
	}
	return visitor(realValue)
}

func (n *navigator) GuessKind(tail []string, guess yaml.Kind) yaml.Kind {
	n.log.Debug("tail %v", tail)
	if len(tail) == 0 && guess == 0 {
		n.log.Debug("end of path, must be a scalar")
		return yaml.ScalarNode
	} else if len(tail) == 0 {
		return guess
	}

	var _, errorParsingInt = strconv.ParseInt(tail[0], 10, 64)
	if tail[0] == "+" || errorParsingInt == nil {
		return yaml.SequenceNode
	}
	if tail[0] == "*" && (guess == yaml.SequenceNode || guess == yaml.MappingNode) {
		return guess
	}
	return yaml.MappingNode
}

func (n *navigator) getOrReplace(original *yaml.Node, expectedKind yaml.Kind) *yaml.Node {
	if original.Kind != expectedKind {
		n.log.Debug("wanted %v but it was %v, overriding", expectedKind, original.Kind)
		return &yaml.Node{Kind: expectedKind}
	}
	return original
}

func (n *navigator) DebugNode(value *yaml.Node) {
	if value == nil {
		n.log.Debug("-- node is nil --")
	} else if n.log.IsEnabledFor(logging.DEBUG) {
		buf := new(bytes.Buffer)
		encoder := yaml.NewEncoder(buf)
		encoder.Encode(value)
		encoder.Close()
		n.log.Debug("Tag: %v", value.Tag)
		n.log.Debug("%v", buf.String())
	}
}

func (n *navigator) recurse(value *yaml.Node, head string, tail []string, visitor VisitorFn) error {
	switch value.Kind {
	case yaml.MappingNode:
		n.log.Debug("its a map with %v entries", len(value.Content)/2)
		if head == "*" {
			return n.splatMap(value, tail, visitor)
		}
		return n.recurseMap(value, head, tail, visitor)
	case yaml.SequenceNode:
		n.log.Debug("its a sequence of %v things!, %v", len(value.Content))
		if head == "*" {
			return n.splatArray(value, tail, visitor)
		} else if head == "+" {
			return n.appendArray(value, tail, visitor)
		}
		return n.recurseArray(value, head, tail, visitor)
	default:
		return nil
	}
}

func (n *navigator) splatMap(value *yaml.Node, tail []string, visitor VisitorFn) error {
	for index, content := range value.Content {
		if index%2 == 0 {
			continue
		}
		content = n.getOrReplace(content, n.GuessKind(tail, content.Kind))
		var err = n.Visit(content, tail, visitor)
		if err != nil {
			return err
		}
	}
	return nil
}

func (n *navigator) recurseMap(value *yaml.Node, head string, tail []string, visitor VisitorFn) error {
	visited, errorVisiting := n.visitMatchingEntries(value.Content, head, func(indexInMap int) error {
		value.Content[indexInMap+1] = n.getOrReplace(value.Content[indexInMap+1], n.GuessKind(tail, value.Content[indexInMap+1].Kind))
		return n.Visit(value.Content[indexInMap+1], tail, visitor)
	})

	if errorVisiting != nil {
		return errorVisiting
	}

	if visited {
		return nil
	}

	//didn't find it, lets add it.
	value.Content = append(value.Content, &yaml.Node{Value: head, Kind: yaml.ScalarNode})
	mapEntryValue := yaml.Node{Kind: n.GuessKind(tail, 0)}
	value.Content = append(value.Content, &mapEntryValue)
	n.log.Debug("adding new node %v", value.Content)
	return n.Visit(&mapEntryValue, tail, visitor)
}

type mapVisitorFn func(int) error

func (n *navigator) visitMatchingEntries(contents []*yaml.Node, key string, visit mapVisitorFn) (bool, error) {
	visited := false

	// value.Content is a concatenated array of key, value,
	// so keys are in the even indexes, values in odd.
	for index := 0; index < len(contents); index = index + 2 {
		content := contents[index]
		n.log.Debug("index %v, checking %v", index, content.Value)
		if n.matchesKey(key, content.Value) {
			errorVisiting := visit(index)
			if errorVisiting != nil {
				return visited, errorVisiting
			}
			visited = true
		}
	}
	return visited, nil
}

func (n *navigator) matchesKey(key string, actual string) bool {
	var prefixMatch = strings.TrimSuffix(key, "*")
	if prefixMatch != key {
		return strings.HasPrefix(actual, prefixMatch)
	}
	return actual == key
}

func (n *navigator) splatArray(value *yaml.Node, tail []string, visitor VisitorFn) error {
	for _, childValue := range value.Content {
		n.log.Debug("processing")
		n.DebugNode(childValue)
		childValue = n.getOrReplace(childValue, n.GuessKind(tail, childValue.Kind))
		var err = n.Visit(childValue, tail, visitor)
		if err != nil {
			return err
		}
	}
	return nil
}

func (n *navigator) appendArray(value *yaml.Node, tail []string, visitor VisitorFn) error {
	var newNode = yaml.Node{Kind: n.GuessKind(tail, 0)}
	value.Content = append(value.Content, &newNode)
	n.log.Debug("appending a new node, %v", value.Content)
	return n.Visit(&newNode, tail, visitor)
}

func (n *navigator) recurseArray(value *yaml.Node, head string, tail []string, visitor VisitorFn) error {
	var index, err = strconv.ParseInt(head, 10, 64) // nolint
	if err != nil {
		return err
	}
	if index >= int64(len(value.Content)) {
		return nil
	}
	value.Content[index] = n.getOrReplace(value.Content[index], n.GuessKind(tail, value.Content[index].Kind))
	return n.Visit(value.Content[index], tail, visitor)
}
