{{ define "members" }}

  {{ range .Members }}
    {{ if not (hiddenMember .)}}
      {{ if not (fieldEmbedded .) }}
        {{ if hasComments .CommentLines }}
        /**
         {{ range .CommentLines }}
         * {{ . }}
         {{ end }}
         */
        {{ end }}
        {{ fieldName . }}{{ if isOptionalMember . }}?{{ end }}: {{ typeDisplayName .Type }};
      {{ end }}
    {{ end }}
  {{ end }}
{{ end }}