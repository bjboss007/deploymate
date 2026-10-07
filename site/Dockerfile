# The DeployMate website: static files behind nginx. Deploy it with DeployMate
# itself (connect the repo, root directory "site", build from this Dockerfile).
FROM nginx:1.27-alpine
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY . /usr/share/nginx/html
RUN rm -f /usr/share/nginx/html/nginx.conf \
 && chmod -R a+rX /usr/share/nginx/html
# ^ nginx runs as an unprivileged user; a checkout made by a service with a strict
#   umask has 0600 files, which would answer 403. Make the content world-readable.
EXPOSE 80
