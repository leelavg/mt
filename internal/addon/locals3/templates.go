package locals3

const serviceTmpl = `apiVersion: v1
kind: Service
metadata:
  name: s3
  namespace: default
  labels:
    app.kubernetes.io/name: local-s3
    app.kubernetes.io/part-of: mt-local-s3
    app.kubernetes.io/created-by: mt
spec:
  ports:
  - name: s3
    protocol: TCP
    port: 9000
    targetPort: 9000
---
apiVersion: discovery.k8s.io/v1
kind: EndpointSlice
metadata:
  name: s3
  namespace: default
  labels:
    kubernetes.io/service-name: s3
    endpointslice.kubernetes.io/managed-by: mt
    app.kubernetes.io/name: local-s3
    app.kubernetes.io/part-of: mt-local-s3
    app.kubernetes.io/created-by: mt
addressType: IPv4
ports:
- name: s3
  protocol: TCP
  port: 9000
endpoints:
- addresses:
  - {{.Address}}
  conditions:
    ready: true
`
