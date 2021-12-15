{{ define "packages" }}

        {{- range .packages -}}

          {{- range (visibleTypes (sortedTypes .Types)) -}}
              {{ if isExportedType . -}}
                  {{ typeDisplayName . }}
              {{- end }}
          {{- end -}}

          {{ range (visibleTypes (sortedTypes .Types))}}
              {{ template "type" .  }}
          {{ end }}
        {{ end }}
{{ end }}
