# Statik arayüz: derleme adımı yok (saf ES modülleri), bu yüzden imaj küçüktür.
# nginx-unprivileged: root olmayan kullanıcı, 8080 portu.
FROM nginxinc/nginx-unprivileged:1.27-alpine
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY index.html /usr/share/nginx/html/index.html
COPY css /usr/share/nginx/html/css
COPY js /usr/share/nginx/html/js
COPY docs /usr/share/nginx/html/docs
EXPOSE 8080
HEALTHCHECK --interval=5s --timeout=3s --retries=10 CMD wget -qO- http://127.0.0.1:8080/ >/dev/null || exit 1
