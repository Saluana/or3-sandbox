FROM debian:bookworm-slim

LABEL org.or3.profile=runtime \
      org.or3.capabilities=exec,files,pty \
      org.or3.dangerous=false

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
      bash \
      ca-certificates \
      curl \
      git \
      jq \
      python3 \
      python3-pip \
      tini && \
      groupadd --gid 10001 sandbox && \
      useradd --uid 10001 --gid 10001 --create-home --shell /bin/bash sandbox && \
      mkdir -p /workspace /cache && \
      chown -R 10001:10001 /workspace /cache /home/sandbox && \
    rm -rf /var/lib/apt/lists/*

ENV HOME=/home/sandbox
USER 10001:10001
WORKDIR /workspace

ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["sleep", "infinity"]
