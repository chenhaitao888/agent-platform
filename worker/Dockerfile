# Supply an immutable Linux base with /bin/sh. Build context contains only the
# cross-compiled Worker and the verified Codex Linux vendor directory.
ARG BASE_IMAGE
FROM ${BASE_IMAGE}
COPY reviewworker /usr/local/bin/reviewworker
COPY codex/ /opt/codex/
RUN chmod 0555 /usr/local/bin/reviewworker /opt/codex/bin/codex && ln -s bin/codex /opt/codex/codex && mkdir /workspaces
USER 65534:65534
WORKDIR /workspaces
ENTRYPOINT ["/usr/local/bin/reviewworker"]
