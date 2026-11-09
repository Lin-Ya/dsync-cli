# 手动测试用的"新设备"：尽量接近全新的 WSL Ubuntu 24.04（预装 git / curl / sudo，普通用户），
# zsh / mise / chezmoi 等一律不预装，交给 dsync init。用法见同目录 README.md。
FROM ubuntu:24.04

ENV LANG=C.UTF-8 DEBIAN_FRONTEND=noninteractive

RUN apt-get update -qq \
 && apt-get install -y -qq --no-install-recommends git curl sudo ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && echo 'ubuntu ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/ubuntu \
 && chmod 440 /etc/sudoers.d/ubuntu

# 访问私有 GitHub 仓库：token 由 run.sh 在运行时挂载到 /run/secrets/github_token（不进镜像）。
# 写在 /etc/gitconfig 而非 ~/.gitconfig：后者由 chezmoi 管理，apply 时会被覆盖。
RUN git config --system credential.https://github.com.helper \
    '!f() { test -r /run/secrets/github_token && printf "username=x-access-token\npassword=%s\n" "$(cat /run/secrets/github_token)"; }; f'

USER ubuntu
WORKDIR /home/ubuntu
