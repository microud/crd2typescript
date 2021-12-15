{{ define "type" }}

{{ renderComments .CommentLines }}
{{ if eq .Kind "Alias" }}
export type {{ .Name.Name }} = {{ if eq (constantsType .) "" }} {{ .Underlying }} {{ else }}{{ constantsType . }}{{ end }};
{{ else }}
export type {{ .Name.Name }} = {
  {{ if .Members }}
  {{ template "members" .}}
  {{ end }}
} {{ if hasEmbeddedTypes . }}{{ range embeddedTypes . }}{{ if not (hiddenMember .) }} & {{ typeDisplayName .Type }}{{ end }}{{ end }}{{ end }};

{{ end }}

{{ end }}
