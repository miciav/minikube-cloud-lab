#!/bin/sh
set -eu

cat >/usr/local/bin/lab-group-http <<'HANDLER'
#!/bin/sh
IFS=' ' read -r method path version || exit
cr=$(printf '\r')
while IFS= read -r header && [ -n "$header" ] && [ "$header" != "$cr" ]; do
    :
done
if [ "$path" = /slow ]; then
    sleep 5
fi
body=$(hostname)
printf 'HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s\n' "$((${#body} + 1))" "$body"
HANDLER
chmod 0755 /usr/local/bin/lab-group-http
systemd-run --unit=lab-group-http /usr/bin/systemd-socket-activate --listen=0.0.0.0:8080 --accept --inetd /usr/local/bin/lab-group-http
