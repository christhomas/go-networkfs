# Samba server for the SMB driver's integration tests.
#
# The SMB driver cannot be tested without a real server, and there is no
# in-process Go SMB server to stand in for one. This image is that server:
# user "smbuser", a writable share "tmp" and a read-only share "tmp2", both
# backed by the same directory.
FROM alpine:3.21

RUN apk add --no-cache samba-server samba-common-tools

COPY smb.conf /etc/samba/smb.conf
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

ENV SMB_USER=smbuser
ENV SMB_PASSWORD=Smbpasswd12345

EXPOSE 445

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
