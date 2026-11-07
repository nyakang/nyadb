# The official 2.5.1 Docker base requires x86-64-v3. This fixture runs its
# unchanged Easysearch distribution, bundled JDK and startup script on Ubuntu
# for hosts with older CPUs. It is a deployment fixture, not another engine.
FROM infinilabs/easysearch@sha256:c2f4df07114180899cccabc4a9bab996732689bf6c44764cff320d692163fedd AS official
FROM ubuntu@sha256:fe81560990fae86dc4743202b2cb4d3df98d0bcf949173cecef15ce1127ef852
RUN apt-get update && apt-get install -y --no-install-recommends \
    openssl curl gosu procps libstdc++6 libzstd1 \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd -g 602 easysearch \
    && useradd -u 602 -g 602 -d /app/easysearch -s /bin/bash easysearch
COPY --from=official /app/easysearch /app/easysearch
COPY --from=official /usr/local/jdk /usr/local/jdk
COPY --from=official /sbin/entrypoint.sh /sbin/entrypoint.sh
ENV PATH="/sbin:/app/easysearch/jdk/bin:/app/easysearch/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
WORKDIR /app/easysearch
ENTRYPOINT ["/sbin/entrypoint.sh"]
CMD ["easysearch"]
