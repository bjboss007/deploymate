# e2e git-path fixture: an nginx static site on the platform port (8080),
# so the worker's default port binding (worker.go: app.Port==0 → 8080)
# publishes it to the preview port without any app-side port config. Built
# via the Dockerfile engine (apps.runtime == ""), so this exercises the
# full git worker path — clone → build → swap → run — with no Railpack or
# buildkit dependency.
FROM nginx:alpine
RUN sed -i 's/listen\s*80;/listen 8080;/' /etc/nginx/conf.d/default.conf
COPY index.html /usr/share/nginx/html/index.html
