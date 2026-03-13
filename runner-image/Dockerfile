FROM boldsoftware/exeuntu

ARG RUNNER_VERSION

LABEL org.opencontainers.image.source="https://github.com/metcalfc/exeunt"
LABEL dev.exeunt.runner-version="${RUNNER_VERSION}"

USER root
RUN mkdir -p /home/exedev/actions-runner \
    && cd /home/exedev/actions-runner \
    && curl -fsSL "https://github.com/actions/runner/releases/download/v${RUNNER_VERSION}/actions-runner-linux-x64-${RUNNER_VERSION}.tar.gz" \
       -o runner.tar.gz \
    && tar xzf runner.tar.gz \
    && rm runner.tar.gz \
    && chown -R exedev:exedev /home/exedev/actions-runner
USER exedev
