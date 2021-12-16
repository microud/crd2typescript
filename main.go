package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/pkg/errors"
	"io"
	"io/ioutil"
	"k8s.io/gengo/parser"
	"k8s.io/gengo/types"
	"k8s.io/klog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"
	"time"
)

var (
	flConfig      = flag.String("config", "", "path to config file")
	flAPIDir      = flag.String("api-dir", "", "api directory (or import path), point this to pkg/apis")
	flTemplateDir = flag.String("template-dir", "template", "path to template/ dir")

	flHTTPAddr           = flag.String("http-addr", "", "start an HTTP server on specified addr to view the result (e.g. :8080)")
	flOutFile            = flag.String("out-file", "", "path to output file to save the result")
	runtimeExternalTypes []*types.Type
)

const (
	docCommentForceIncludes = "// +gencrdrefdocs:force"
)

type generatorConfig struct {
	// HiddenMemberFields hides fields with specified names on all types.
	HiddenMemberFields []string `json:"hideMemberFields"`

	// HideTypePatterns hides types matching the specified patterns from the
	// output.
	HideTypePatterns []string `json:"hideTypePatterns"`

	// ExternalPackages lists recognized external package references and how to
	// link to them.
	ExternalPackages []externalPackage `json:"externalPackages"`

	ExternalTypes map[string]map[string]string `json:"externalTypes"`

	TypeReplacements map[string]string `json:"typeReplacements"`

	SliceTemplate string `json:"sliceTemplate"`
}

type externalPackage struct {
	TypeMatchPrefix string `json:"typeMatchPrefix"`
}

type apiPackage struct {
	apiGroup   string
	apiVersion string
	GoPackages []*types.Package
	Types      []*types.Type // because multiple 'types.Package's can add types to an apiVersion
	Constants  []*types.Type
}

func (v *apiPackage) identifier() string { return fmt.Sprintf("%s/%s", v.apiGroup, v.apiVersion) }

func init() {
	klog.InitFlags(nil)
	flag.Set("alsologtostderr", "true") // for klog
	flag.Parse()

	if *flConfig == "" {
		panic("-config not specified")
	}
	if *flAPIDir == "" {
		panic("-api-dir not specified")
	}
	if *flHTTPAddr == "" && *flOutFile == "" {
		panic("-out-file or -http-addr must be specified")
	}
	if *flHTTPAddr != "" && *flOutFile != "" {
		panic("only -out-file or -http-addr can be specified")
	}
	if err := resolveTemplateDir(*flTemplateDir); err != nil {
		panic(err)
	}

}

func resolveTemplateDir(dir string) error {
	path, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if fi, err := os.Stat(path); err != nil {
		return errors.Wrapf(err, "cannot read the %s directory", path)
	} else if !fi.IsDir() {
		return errors.Errorf("%s path is not a directory", path)
	}
	return nil
}

func main() {
	wd, err := os.Getwd()
	if err != nil {
		klog.Fatalf("failed to local current working directory")
	}
	klog.Infof("working directory is %s", wd)
	defer klog.Flush()

	f, err := os.Open(*flConfig)
	if err != nil {
		klog.Fatalf("failed to open config file: %+v", err)
	}
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	var config generatorConfig
	if err := d.Decode(&config); err != nil {
		klog.Fatalf("failed to parse config file: %+v", err)
	}

	klog.Infof("parsing go packages in directory %s", *flAPIDir)
	pkgs, err := parseAPIPackages(*flAPIDir)
	if err != nil {
		klog.Fatal(err)
	}
	if len(pkgs) == 0 {
		klog.Fatalf("no API packages found in %s", *flAPIDir)
	}

	apiPackages, err := combineAPIPackages(pkgs)
	if err != nil {
		klog.Fatal(err)
	}

	mkOutput := func() (string, error) {
		var b bytes.Buffer
		err := render(&b, apiPackages, config)
		if err != nil {
			return "", errors.Wrap(err, "failed to render the result")
		}

		// remove trailing whitespace from each html line for markdown renderers
		s := regexp.MustCompile(`(?m)^\s+`).ReplaceAllString(b.String(), "")
		return s, nil
	}

	if *flOutFile != "" {
		dir := filepath.Dir(*flOutFile)
		if err := os.MkdirAll(dir, 0755); err != nil {
			klog.Fatalf("failed to create dir %s: %v", dir, err)
		}
		s, err := mkOutput()
		if err != nil {
			klog.Fatalf("failed: %+v", err)
		}
		if err := ioutil.WriteFile(*flOutFile, []byte(s), 0644); err != nil {
			klog.Fatalf("failed to write to out file: %v", err)
		}
		klog.Infof("written to %s", *flOutFile)
	}

	if *flHTTPAddr != "" {
		h := func(w http.ResponseWriter, r *http.Request) {
			now := time.Now()
			defer func() { klog.Infof("request took %v", time.Since(now)) }()
			s, err := mkOutput()
			if err != nil {
				fmt.Fprintf(w, "error: %+v", err)
				klog.Warningf("failed: %+v", err)
			}
			if _, err := fmt.Fprint(w, s); err != nil {
				klog.Warningf("response write error: %v", err)
			}
		}
		http.HandleFunc("/", h)
		klog.Infof("server listening at %s", *flHTTPAddr)
		klog.Fatal(http.ListenAndServe(*flHTTPAddr, nil))
	}
}

// groupName extracts the "//+groupName" meta-comment from the specified
// package's comments, or returns empty string if it cannot be found.
func groupName(pkg *types.Package) string {
	m := types.ExtractCommentTags("+", pkg.Comments)
	v := m["groupName"]
	if len(v) == 1 {
		return v[0]
	}
	return ""
}

func parseAPIPackages(dir string) ([]*types.Package, error) {
	b := parser.New()
	// the following will silently fail (turn on -v=4 to see logs)
	if err := b.AddDirRecursive(*flAPIDir); err != nil {
		return nil, err
	}
	scan, err := b.FindTypes()
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse pkgs and types")
	}
	var pkgNames []string
	for p := range scan {
		pkg := scan[p]
		klog.V(3).Infof("trying package=%v groupName=%s", p, groupName(pkg))

		// Do not pick up packages that are in vendor/ as API packages. (This
		// happened in knative/eventing-sources/vendor/..., where a package
		// matched the pattern, but it didn't have a compatible import path).
		if isVendorPackage(pkg) {
			klog.V(3).Infof("package=%v coming from vendor/, ignoring.", p)
			continue
		}

		if groupName(pkg) != "" && len(pkg.Types) > 0 || containsString(pkg.DocComments, docCommentForceIncludes) {
			klog.V(3).Infof("package=%v has groupName and has types", p)
			pkgNames = append(pkgNames, p)
		}
	}
	sort.Strings(pkgNames)
	var pkgs []*types.Package
	for _, p := range pkgNames {
		klog.Infof("using package=%s", p)
		pkgs = append(pkgs, scan[p])
	}
	return pkgs, nil
}

func containsString(sl []string, str string) bool {
	for _, s := range sl {
		if str == s {
			return true
		}
	}
	return false
}

// combineAPIPackages groups the Go packages by the <apiGroup+apiVersion> they
// offer, and combines the types in them.
func combineAPIPackages(pkgs []*types.Package) ([]*apiPackage, error) {
	pkgMap := make(map[string]*apiPackage)
	var pkgIds []string

	flattenTypes := func(typeMap map[string]*types.Type) []*types.Type {
		typeList := make([]*types.Type, 0, len(typeMap))

		for _, t := range typeMap {
			typeList = append(typeList, t)
		}

		return typeList
	}

	for _, pkg := range pkgs {
		apiGroup, apiVersion, err := apiVersionForPackage(pkg)
		if err != nil {
			return nil, errors.Wrapf(err, "could not get apiVersion for package %s", pkg.Path)
		}

		typeList := make([]*types.Type, 0, len(pkg.Types))
		for _, t := range pkg.Types {
			typeList = append(typeList, t)
		}

		id := fmt.Sprintf("%s/%s", apiGroup, apiVersion)
		v, ok := pkgMap[id]
		if !ok {
			pkgMap[id] = &apiPackage{
				apiGroup:   apiGroup,
				apiVersion: apiVersion,
				Types:      flattenTypes(pkg.Types),
				Constants:  flattenTypes(pkg.Constants),
				GoPackages: []*types.Package{pkg},
			}
			pkgIds = append(pkgIds, id)
		} else {
			v.Types = append(v.Types, flattenTypes(pkg.Types)...)
			v.Constants = append(v.Types, flattenTypes(pkg.Constants)...)
			v.GoPackages = append(v.GoPackages, pkg)
		}
	}

	sort.Sort(sort.StringSlice(pkgIds))

	out := make([]*apiPackage, 0, len(pkgMap))
	for _, id := range pkgIds {
		out = append(out, pkgMap[id])
	}
	return out, nil
}

// isVendorPackage determines if package is coming from vendor/ dir.
func isVendorPackage(pkg *types.Package) bool {
	vendorPattern := string(os.PathSeparator) + "vendor" + string(os.PathSeparator)
	return strings.Contains(pkg.SourcePath, vendorPattern)
}

func findTypeReferences(pkgs []*apiPackage) map[*types.Type][]*types.Type {
	m := make(map[*types.Type][]*types.Type)
	for _, pkg := range pkgs {
		for _, typ := range pkg.Types {
			for _, member := range typ.Members {
				t := member.Type
				t = tryDereference(t)
				m[t] = append(m[t], typ)
			}
		}
	}
	return m
}

func hiddenMember(m types.Member, c generatorConfig) bool {
	for _, v := range c.HiddenMemberFields {
		if m.Name == v {
			return true
		}
	}
	return false
}

func packageDisplayName(pkg *types.Package, apiVersions map[string]string) string {
	apiGroupVersion, ok := apiVersions[pkg.Path]
	if ok {
		return apiGroupVersion
	}
	return pkg.Path // go import path
}

func filterCommentTags(comments []string) []string {
	var out []string
	for _, v := range comments {
		if !strings.HasPrefix(strings.TrimSpace(v), "+") {
			out = append(out, v)
		}
	}
	return out
}

func isOptionalMember(m types.Member) bool {
	tags := types.ExtractCommentTags("+", m.CommentLines)
	_, ok := tags["optional"]
	return ok
}

func apiVersionForPackage(pkg *types.Package) (string, string, error) {
	group := groupName(pkg)
	version := pkg.Name // assumes basename (i.e. "v1" in "core/v1") is apiVersion
	r := `^v\d+((alpha|beta)\d+)?$`
	if !regexp.MustCompile(r).MatchString(version) {
		return "", "", errors.Errorf("cannot infer kubernetes apiVersion of go package %s (basename %q doesn't match expected pattern %s that's used to determine apiVersion)", pkg.Path, version, r)
	}
	return group, version, nil
}

// extractTypeToPackageMap creates a *types.Type map to apiPackage
func extractTypeToPackageMap(pkgs []*apiPackage) map[*types.Type]*apiPackage {
	out := make(map[*types.Type]*apiPackage)
	for _, ap := range pkgs {
		for _, t := range ap.Types {
			out[t] = ap
		}
		for _, t := range ap.Constants {
			out[t] = ap
		}
	}
	return out
}

// packageMapToList flattens the map.
func packageMapToList(pkgs map[string]*apiPackage) []*apiPackage {
	// TODO(ahmetb): we should probably not deal with maps, this type can be
	// a list everywhere.
	out := make([]*apiPackage, 0, len(pkgs))
	for _, v := range pkgs {
		out = append(out, v)
	}
	return out
}

func render(w io.Writer, pkgs []*apiPackage, config generatorConfig) error {
	references := findTypeReferences(pkgs)
	typePkgMap := extractTypeToPackageMap(pkgs)

	t, err := template.New("").Funcs(map[string]interface{}{
		"isExportedType":     isExportedType,
		"fieldName":          fieldName,
		"fieldEmbedded":      fieldEmbedded,
		"hasEmbeddedTypes":   hasEmbeddedTypes,
		"embeddedTypes":      embeddedTypes,
		"typeIdentifier":     func(t *types.Type) string { return typeIdentifier(t) },
		"typeDisplayName":    func(t *types.Type) string { return typeDisplayName(t, config, typePkgMap) },
		"visibleTypes":       func(t []*types.Type) []*types.Type { return visibleTypes(t, config) },
		"hasComments":        hasComments,
		"renderComments":     func(s []string) string { return renderComments(s) },
		"packageDisplayName": func(p *apiPackage) string { return p.identifier() },
		"apiGroup":           func(t *types.Type) string { return apiGroupForType(t, typePkgMap) },
		"packageAnchorID": func(p *apiPackage) string {
			// TODO(ahmetb): currently this is the same as packageDisplayName
			// func, and it's fine since it retuns valid DOM id strings like
			// 'serving.knative.dev/v1alpha1' which is valid per HTML5, except
			// spaces, so just trim those.
			return strings.Replace(p.identifier(), " ", "", -1)
		},
		"sortedTypes":      sortTypes,
		"typeReferences":   func(t *types.Type) []*types.Type { return typeReferences(t, config, references) },
		"hiddenMember":     func(m types.Member) bool { return hiddenMember(m, config) },
		"isLocalType":      isLocalType,
		"isOptionalMember": isOptionalMember,
		"constantsOfType":  func(t *types.Type) []*types.Type { return constantsOfType(t, typePkgMap[t]) },
		"constantsType": func(t *types.Type) string {
			typs := constantsOfType(t, typePkgMap[t])
			var values []string
			for _, typ := range typs {
				if typ.ConstValue != nil {
					values = append(values, fmt.Sprintf("'%s'", *typ.ConstValue))
				}
			}

			return strings.Join(values, " | ")
		},
	}).ParseGlob(filepath.Join(*flTemplateDir, "*.tpl"))
	if err != nil {
		return errors.Wrap(err, "parse error")
	}

	return errors.Wrap(t.ExecuteTemplate(w, "packages", map[string]interface{}{
		"packages": pkgs,
		"config":   config,
	}), "template execution error")
}
