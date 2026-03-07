#cloud-config
users:
  - default
  - name: __SSH_USER__
    gecos: or3 sandbox user
    groups: [sudo, docker]
    shell: /bin/bash
    sudo: ALL=(ALL) NOPASSWD:ALL
    ssh_authorized_keys:
      - __SSH_PUBLIC_KEY__

package_update: true
package_upgrade: false
packages:
  - ca-certificates
  - curl
  - docker.io
  - git
  - jq
  - nodejs
  - npm
  - openssh-server
  - python3
  - python3-pip
  - sudo
  - xvfb
  - libasound2
  - libatk-bridge2.0-0
  - libcups2
  - libdrm2
  - libgbm1
  - libgtk-3-0
  - libnss3
  - libxdamage1
  - libxkbcommon0
  - libxrandr2

write_files:
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

runcmd:
  - mkdir -p /var/lib/or3
  - systemctl daemon-reload
  - systemctl enable ssh
  - systemctl enable docker
  - systemctl enable or3-bootstrap.service
  - systemctl start docker
  - systemctl start or3-bootstrap.service

final_message: "or3 guest bootstrap complete"
