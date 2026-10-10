FROM --platform=linux/amd64 ubuntu:22.04

ENV DEBIAN_FRONTEND=noninteractive \
    TZ=Asia/Shanghai \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8

RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        bash \
        ca-certificates \
        curl \
        git \
        iproute2 \
        less \
        net-tools \
        openssh-server \
        procps \
        rsync \
        supervisor \
        telnet \
        tzdata \
        vim-tiny \
        wget; \
    rm -rf /var/lib/apt/lists/*; \
    mkdir -p /app/current /app/releases /data/auths /data/plugins /data/logs/app /data/logs/supervisor /run/sshd; \
    sed -i 's/^#\?PermitRootLogin .*/PermitRootLogin yes/' /etc/ssh/sshd_config; \
    sed -i 's/^#\?PasswordAuthentication .*/PasswordAuthentication yes/' /etc/ssh/sshd_config

COPY deploy/monolith/base/supervisord.conf /etc/supervisor/conf.d/monolith.conf
COPY deploy/monolith/base/entrypoint.sh /usr/local/bin/monolith-entrypoint

RUN chmod +x /usr/local/bin/monolith-entrypoint

WORKDIR /app/current

VOLUME ["/data", "/app"]

EXPOSE 22 8317

ENTRYPOINT ["/usr/local/bin/monolith-entrypoint"]
