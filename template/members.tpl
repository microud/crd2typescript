{{ define "members" }}

  {{ range .Members }}
    {{ if not (hiddenMember .)}}
      {{ if not (fieldEmbedded .) }}
        {{ renderComments .CommentLines }}
        {{ fieldName . }}{{ if isOptionalMember . }}?{{ end }}: {{ typeDisplayName .Type }};
      {{ end }}
    {{ end }}
  {{ end }}
{{ end }}