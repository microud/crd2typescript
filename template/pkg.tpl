{{ define "packages" }}

        type ObjectMetadata = {
          name: string;
          resourceVersion: string;
          labels: Record<string, string>;
        }

        {{- range .packages -}}
          {{ range (visibleTypes (sortedTypes .Types))}}
              {{ template "type" .  }}
          {{ end }}


        export type CustomResourceDefinition<T extends { metadata: unknown; spec: unknown; status: unknown}> = {
          apiVersion: string;
          metadata: T['metadata'];
          spec: T['spec'];
          status: T['status'];
        }

        export type ResourceDefinitions = {
           {{ range (visibleTypes (sortedTypes .Types)) }}
               {{ if isExportedType . }}
                    '{{ typeDisplayName . }}': CustomResourceDefinition<{{ typeDisplayName . }}>;
               {{ end }}
           {{ end }}
        }
        {{ end }}

        export type CustomResourceKinds = keyof ResourceDefinitions;

        export type CustomResources<
          K extends CustomResourceKinds
        > = ResourceDefinitions[K];
{{ end }}
