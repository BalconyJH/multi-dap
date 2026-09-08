package inspection

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

func newExpression(text string) (Expression, error) {
	// Reject separators before trimming. Otherwise a trailing newline would be
	// silently removed and become part of the command assembled by a backend.
	for _, r := range text {
		if r == 0 || r == '\r' || r == '\n' || r == ';' || unicode.IsControl(r) {
			return Expression{}, fmt.Errorf("%w: contains a command separator or control character", ErrInvalidExpression)
		}
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return Expression{}, fmt.Errorf("%w: empty", ErrInvalidExpression)
	}
	return Expression{text: text}, nil
}

func rootLocator(name string) (ValueLocator, error) {
	if !isRootName(name) {
		return ValueLocator{}, fmt.Errorf("%w: invalid root identifier %q", ErrBackendContract, name)
	}
	return ValueLocator{expression: name}, nil
}

// isRootName accepts the identifier syntax emitted by MULTI for ordinary
// roots, plus its root-array form (for example, "buffer[0]"). The array
// index remains part of the opaque backend name so a frontend's evaluateName
// exactly matches the expression used to retrieve children.
func isRootName(name string) bool {
	if isIdentifier(name) {
		return true
	}
	open := strings.IndexByte(name, '[')
	if open <= 0 || !strings.HasSuffix(name, "]") || !isIdentifier(name[:open]) {
		return false
	}
	index := name[open+1 : len(name)-1]
	if index == "" {
		return false
	}
	for _, r := range index {
		if !isASCIIDigit(r) {
			return false
		}
	}
	_, err := strconv.ParseUint(index, 10, 64)
	return err == nil
}

func evaluatedLocator(expression Expression) ValueLocator {
	return ValueLocator{expression: expression.text}
}

func childLocator(parent ValueLocator, access Access) (ValueLocator, error) {
	if parent.expression == "" {
		return ValueLocator{}, fmt.Errorf("%w: empty parent locator", ErrBackendContract)
	}
	parenthesized := "(" + parent.expression + ")"
	switch access.Kind {
	case AccessMember:
		if !isIdentifier(access.Name) {
			return ValueLocator{}, fmt.Errorf("%w: invalid member identifier %q", ErrBackendContract, access.Name)
		}
		if !access.HasIndex && access.Index != 0 {
			return ValueLocator{}, fmt.Errorf("%w: member index requires HasIndex", ErrBackendContract)
		}
		expression := parenthesized + "." + access.Name
		if access.HasIndex {
			expression += fmt.Sprintf("[%d]", access.Index)
		}
		return ValueLocator{expression: expression}, nil
	case AccessPointerMember:
		if !isIdentifier(access.Name) {
			return ValueLocator{}, fmt.Errorf("%w: invalid pointer-member identifier %q", ErrBackendContract, access.Name)
		}
		if !access.HasIndex && access.Index != 0 {
			return ValueLocator{}, fmt.Errorf("%w: pointer-member index requires HasIndex", ErrBackendContract)
		}
		expression := parenthesized + "->" + access.Name
		if access.HasIndex {
			expression += fmt.Sprintf("[%d]", access.Index)
		}
		return ValueLocator{expression: expression}, nil
	case AccessIndex:
		if access.Name != "" || access.HasIndex {
			return ValueLocator{}, fmt.Errorf("%w: index access contains extra data", ErrBackendContract)
		}
		return ValueLocator{expression: fmt.Sprintf("%s[%d]", parenthesized, access.Index)}, nil
	case AccessDereference:
		if access.Name != "" || access.Index != 0 || access.HasIndex {
			return ValueLocator{}, fmt.Errorf("%w: dereference contains extra data", ErrBackendContract)
		}
		return ValueLocator{expression: "*" + parenthesized}, nil
	default:
		return ValueLocator{}, fmt.Errorf("%w: invalid child access", ErrBackendContract)
	}
}

func isIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for index, r := range value {
		if index == 0 {
			if r != '_' && r != '$' && !isASCIILetter(r) {
				return false
			}
			continue
		}
		if r != '_' && r != '$' && !isASCIILetter(r) && !isASCIIDigit(r) {
			return false
		}
	}
	return true
}

func isASCIILetter(r rune) bool { return r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' }

func isASCIIDigit(r rune) bool { return r >= '0' && r <= '9' }
