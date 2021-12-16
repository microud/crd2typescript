package main

import (
	"bytes"
	"fmt"
	"k8s.io/gengo/types"
	"k8s.io/klog"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"unicode"
)

func typeIdentifier(t *types.Type) string {
	t = tryDereference(t)
	return t.Name.String() // {PackagePath.Name}
}

// apiGroupForType looks up apiGroup for the given type
func apiGroupForType(t *types.Type, typePkgMap map[*types.Type]*apiPackage) string {
	t = tryDereference(t)

	v := typePkgMap[t]
	if v == nil {
		klog.Warningf("WARNING: cannot read apiVersion for %s from type=>pkg map", t.Name.String())
		return "<UNKNOWN_API_GROUP>"
	}

	return v.identifier()
}

// tryDereference returns the underlying type when t is a pointer, map, or slice.
func tryDereference(t *types.Type) *types.Type {
	for t.Elem != nil {
		t = t.Elem
	}
	return t
}

// finalUnderlyingTypeOf walks the type hierarchy for t and returns
// its base type (i.e. the type that has no further underlying type).
func finalUnderlyingTypeOf(t *types.Type) *types.Type {
	for {
		if t.Underlying == nil {
			return t
		}

		t = t.Underlying
	}
}

func replaceTypeName(c generatorConfig, s string) string {
	result, ok := c.TypeReplacements[s]

	if ok {
		return result
	}

	return s
}

//func externalType(c generatorConfig, t *types.Type) *types.Type {
//
//}

func addExternalType(t *types.Type) {
	for t.Kind == types.Pointer || t.Kind == types.Slice {
		t = t.Elem
	}

	runtimeExternalTypes = append(runtimeExternalTypes, t)
}

func isExternalType(c generatorConfig, id string) bool {
	for _, v := range c.ExternalPackages {
		r, err := regexp.Compile(v.TypeMatchPrefix)
		if err != nil {
			return false
		}

		if r.MatchString(id) {
			return true
		}
	}

	return false
}

func externalTypeReplacement(c generatorConfig, t *types.Type) string {
	for t.Kind == types.Pointer || t.Kind == types.Slice {
		t = t.Elem
	}

	pkg, ok := c.ExternalTypes[t.Name.Package]

	if ok {
		r, ok := pkg[t.Name.Name]
		if ok {
			return r
		}
	}

	return t.Name.Name
}

func typeDisplayName(t *types.Type, c generatorConfig, typePkgMap map[*types.Type]*apiPackage) string {
	s := typeIdentifier(t)

	if isLocalType(t, typePkgMap) {
		s = tryDereference(t).Name.Name
	}

	if t.Kind == types.Pointer {
		s = strings.TrimLeft(s, "*")
	}

	if isExternalType(c, s) {
		s = externalTypeReplacement(c, t)
	}

	switch t.Kind {
	case types.Struct,
		types.Interface,
		types.Alias,
		types.Pointer,
		types.Slice,
		types.Builtin:
		// noop
	case types.Map:
		// return original name
		return fmt.Sprintf("Record<%s, %s>", t.Key.Name.Name, replaceTypeName(c, t.Elem.Name.Name))
	case types.DeclarationOf:
		// For constants, we want to display the value
		// rather than the name of the constant, since the
		// value is what users will need to write into YAML
		// specs.
		if t.ConstValue != nil {
			u := finalUnderlyingTypeOf(t)
			// Quote string constants to make it clear to the documentation reader.
			if u.Kind == types.Builtin && u.Name.Name == "string" {
				return strconv.Quote(*t.ConstValue)
			}

			return *t.ConstValue
		}
		klog.Fatalf("type %s is a non-const declaration, which is unhandled", t.Name)
	default:
		//it seems imported third lib types missed here.
		klog.Fatalf("type %s has kind=%v which is unhandled", t.Name, t.Kind)
	}

	s = replaceTypeName(c, s)

	if t.Kind == types.Slice {
		tpl, err := template.New("").Parse(c.SliceTemplate)
		if err != nil {
			return s
		}
		var b bytes.Buffer
		err = tpl.Execute(&b, map[string]interface{}{
			"type": s,
		})
		if err != nil {
			return s
		}

		s = b.String()
	}

	return s
}

func hideType(t *types.Type, c generatorConfig) bool {
	for _, pattern := range c.HideTypePatterns {
		if regexp.MustCompile(pattern).MatchString(t.Name.String()) {
			return true
		}
	}
	if !isExportedType(t) && unicode.IsLower(rune(t.Name.Name[0])) {
		// types that start with lowercase
		return true
	}
	return false
}

func typeReferences(t *types.Type, c generatorConfig, references map[*types.Type][]*types.Type) []*types.Type {
	var out []*types.Type
	m := make(map[*types.Type]struct{})
	for _, ref := range references[t] {
		if !hideType(ref, c) {
			m[ref] = struct{}{}
		}
	}
	for k := range m {
		out = append(out, k)
	}
	sortTypes(out)
	return out
}

func sortTypes(typs []*types.Type) []*types.Type {
	sort.Slice(typs, func(i, j int) bool {
		t1, t2 := typs[i], typs[j]
		if isExportedType(t1) && !isExportedType(t2) {
			return true
		} else if !isExportedType(t1) && isExportedType(t2) {
			return false
		}
		return t1.Name.String() < t2.Name.String()
	})
	return typs
}

func visibleTypes(in []*types.Type, c generatorConfig) []*types.Type {
	var out []*types.Type
	for _, t := range in {
		if !hideType(t, c) {
			out = append(out, t)
		}
	}
	return out
}

func isExportedType(t *types.Type) bool {
	// TODO(ahmetb) use types.ExtractSingleBoolCommentTag() to parse +genclient
	// https://godoc.org/k8s.io/gengo/types#ExtractCommentTags
	res := strings.Contains(strings.Join(t.SecondClosestCommentLines, "\n"), "+kubebuilder:object:root=true")
	return res
}

func fieldName(m types.Member) string {
	v := reflect.StructTag(m.Tags).Get("json")
	v = strings.TrimSuffix(v, ",omitempty")
	v = strings.TrimSuffix(v, ",inline")
	if v != "" {
		return v
	}
	return m.Name
}

func fieldEmbedded(m types.Member) bool {
	return strings.Contains(reflect.StructTag(m.Tags).Get("json"), ",inline")
}

func hasEmbeddedTypes(t types.Type) bool {
	for _, m := range t.Members {
		if fieldEmbedded(m) {
			return true
		}
	}

	return false
}

func embeddedTypes(t types.Type) (ms []types.Member) {
	for _, member := range t.Members {
		if fieldEmbedded(member) {
			ms = append(ms, member)
		}
	}

	return
}

func isLocalType(t *types.Type, typePkgMap map[*types.Type]*apiPackage) bool {
	t = tryDereference(t)
	_, ok := typePkgMap[t]
	return ok
}

func hasComments(s []string) bool {
	s = filterCommentTags(s)
	if len(s) == 0 || (len(s) == 1 && s[0] == "") {
		return false
	}

	return true
}

func renderComments(s []string) string {
	s = filterCommentTags(s)
	if len(s) == 0 || (len(s) == 1 && s[0] == "") {
		return ""
	}

	for i := range s {
		s[i] = " * " + s[i]
	}
	doc := strings.Join(s, "\n")

	return "/**\n" + doc + "\n */"
}

// constantsOfType finds all the constants in pkg that have the
// same underlying type as t. This is intended for use by enum
// type validation, where users need to specify one of a specific
// set of constant values for a field.
func constantsOfType(t *types.Type, pkg *apiPackage) []*types.Type {
	constants := []*types.Type{}

	for _, c := range pkg.Constants {
		if c.Underlying == t {
			constants = append(constants, c)
		}
	}

	return sortTypes(constants)
}

// TODO extract external types
//func externalTypes(c generatorConfig, pkg *apiPackage) []*types.Type {
//	ts := []*types.Type{}
//	for i, t := range pkg.Types {
//
//	}
//}
