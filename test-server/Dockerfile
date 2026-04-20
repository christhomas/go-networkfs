FROM alpine:3.19

# Credentials applied across SSH/SFTP, FTP, WebDAV, and SMB. Passed in
# from docker-compose.yml via build args so the values are visible at
# the top level; override there (or via TEST_USER / TEST_PASSWORD env
# vars) rather than patching this Dockerfile.
ARG TEST_USER=testuser
ARG TEST_PASSWORD=testpass

RUN apk add --no-cache \
    openssh-server \
    vsftpd \
    apache2 \
    apache2-webdav \
    apache2-utils \
    samba \
    bash

# Create test user (same creds for all services)
RUN adduser -D -s /bin/sh "${TEST_USER}" && \
    echo "${TEST_USER}:${TEST_PASSWORD}" | chpasswd

# Create data directories
RUN mkdir -p /data/sftp /data/ftp /data/webdav /data/smb && \
    chown "${TEST_USER}:${TEST_USER}" /data/sftp /data/ftp /data/webdav /data/smb

# SSH/SFTP config
RUN ssh-keygen -A && \
    sed -i 's/#PasswordAuthentication yes/PasswordAuthentication yes/' /etc/ssh/sshd_config && \
    sed -i 's/PasswordAuthentication no/PasswordAuthentication yes/' /etc/ssh/sshd_config && \
    echo "PasswordAuthentication yes" >> /etc/ssh/sshd_config && \
    echo "PermitRootLogin no" >> /etc/ssh/sshd_config && \
    echo "Subsystem sftp internal-sftp" >> /etc/ssh/sshd_config && \
    echo "Match User ${TEST_USER}" >> /etc/ssh/sshd_config && \
    echo "  ForceCommand internal-sftp" >> /etc/ssh/sshd_config && \
    echo "  AllowTcpForwarding no" >> /etc/ssh/sshd_config

# FTP config. vsftpd authenticates against the system user, so the
# ARG-created account above is enough — no username baked into the conf.
COPY vsftpd.conf /etc/vsftpd/vsftpd.conf

# WebDAV config
COPY webdav.conf /etc/apache2/conf.d/webdav.conf
RUN htpasswd -cb /etc/apache2/webdav.htpasswd "${TEST_USER}" "${TEST_PASSWORD}" && \
    chown apache:apache /data/webdav

# Samba config. smb.conf references the user as __TEST_USER__ so the
# build step can substitute whatever was passed in via ARG.
COPY smb.conf /etc/samba/smb.conf
RUN sed -i "s/__TEST_USER__/${TEST_USER}/g" /etc/samba/smb.conf && \
    printf '%s\n%s\n' "${TEST_PASSWORD}" "${TEST_PASSWORD}" | smbpasswd -a -s "${TEST_USER}"

# Startup script
COPY start.sh /start.sh
RUN chmod +x /start.sh

EXPOSE 22 21 8080 445 21000-21010

CMD ["/start.sh"]
