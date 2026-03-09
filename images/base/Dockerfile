FROM mcr.microsoft.com/playwright:v1.51.1-noble

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
      ca-certificates \
      curl \
      docker.io \
      git \
      jq \
      python3-pip \
      sudo \
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
