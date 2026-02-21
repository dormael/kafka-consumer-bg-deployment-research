

### port forward

- `kubectl -n monitoring port-forward svc/kube-prometheus-stack-prometheus --address=0.0.0.0 9090:9090`
  - [prometheus](http://localhost:9090/)
- `kubectl -n monitoring port-forward svc/kube-prometheus-stack-grafana --address=0.0.0.0 8080:80`
  - [grafana](http://localhost:8080/)
    - admin
    - admin???
