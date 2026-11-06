FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata \
 && adduser -D -h /home/demo -s /bin/sh demo \
 && mkdir -p /demo

COPY perch /usr/local/bin/perch
COPY demo-ca.pem /usr/local/share/ca-certificates/demo-ca.crt
RUN chmod 0755 /usr/local/bin/perch \
 && update-ca-certificates \
 && printf 'Sample attachment for the perch live demo.\n' > /home/demo/sample.txt \
 && chown -R demo:demo /demo /home/demo

USER demo
WORKDIR /home/demo
ENV TERM=xterm-256color
CMD ["sleep", "infinity"]
