#cloud-config
users:
  - default
  - name: __AGENT_USER__
    gecos: or3 guest agent user
    system: true
    shell: /usr/sbin/nologin
  - name: __SANDBOX_USER__
    gecos: or3 sandbox user
    groups: __SANDBOX_GROUPS__
    shell: /bin/bash
__SANDBOX_SUDO_LINE____SSH_AUTHORIZED_KEYS_BLOCK__

package_update: true
package_upgrade: false
packages:
__PROFILE_PACKAGES__

write_files:
  - path: /usr/local/bin/or3-guest-agent
    permissions: "0755"
    owner: root:root
    encoding: b64
    content: __GUEST_AGENT_BINARY_BASE64__
  - path: /etc/systemd/system/or3-guest-agent.service
    permissions: "0644"
    owner: root:root
    content: |
      __GUEST_AGENT_SERVICE__
  - path: /usr/local/bin/or3-bootstrap.sh
    permissions: "0755"
    owner: root:root
    content: |
      __BOOTSTRAP_SCRIPT__
  - path: /etc/systemd/system/or3-bootstrap.service
    permissions: "0644"
    owner: root:root
    content: |
      __BOOTSTRAP_SERVICE__
  - path: /etc/or3/profile-manifest.json
    permissions: "0644"
    owner: root:root
    content: |
      __PROFILE_MANIFEST_JSON__

runcmd:
  - mkdir -p /var/lib/or3
  - mkdir -p /etc/or3
  - systemctl daemon-reload
__SSH_ENABLE_COMMANDS____PROFILE_ENABLE_COMMANDS__  - systemctl enable or3-guest-agent.service
  - systemctl enable or3-bootstrap.service
  - systemctl start or3-guest-agent.service
  - systemctl start or3-bootstrap.service

final_message: "or3 guest bootstrap complete for profile __PROFILE_NAME__"
