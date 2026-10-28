# 金手指管家 Android 构建环境（Docker）
# 用途：在容器内完成 gomobile bind（Go 服务 → AAR）与 Gradle assembleDebug（→ APK）。
# 背景：宿主（Windows）无 JDK / Android SDK / NDK / Gradle，故全部在镜像内自备。
#
# 构建：docker build -t gfa-android-build -f docker/android/Dockerfile docker/android
# 运行：见 docker/android/build.sh

FROM docker.m.daocloud.io/eclipse-temurin:17-jdk-jammy

ENV DEBIAN_FRONTEND=noninteractive
# apt 换阿里云源（Ubuntu jammy，amd64）
RUN sed -i 's@//.*archive.ubuntu.com@//mirrors.aliyun.com@g; s@//.*security.ubuntu.com@//mirrors.aliyun.com@g' /etc/apt/sources.list
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates curl wget unzip git bash python3 xz-utils \
    && rm -rf /var/lib/apt/lists/*

# ---- Go（与 go.mod 声明版本对齐）----
ARG GO_VERSION=1.26.5
RUN set -eux; \
    curl -fsSL "https://mirrors.aliyun.com/golang/go${GO_VERSION}.linux-amd64.tar.gz" -o /tmp/go.tgz; \
    tar -C /usr/local -xzf /tmp/go.tgz; \
    rm /tmp/go.tgz
ENV GOPATH=/root/go
ENV PATH=/usr/local/go/bin:${GOPATH}/bin:${PATH}
ENV GOPROXY=https://goproxy.cn,https://proxy.golang.org,direct
ENV GOCACHE=/root/.cache/go-build

# ---- Android SDK（cmdline-tools 固定历史版本，URL 长期有效）----
ENV ANDROID_HOME=/opt/android-sdk
ENV ANDROID_SDK_ROOT=/opt/android-sdk
RUN set -eux; \
    mkdir -p "${ANDROID_HOME}/cmdline-tools"; \
    curl -fsSL "https://dl.google.com/android/repository/commandlinetools-linux-11076708_latest.zip" -o /tmp/cmdtools.zip; \
    unzip -q /tmp/cmdtools.zip -d "${ANDROID_HOME}/cmdline-tools"; \
    mv "${ANDROID_HOME}/cmdline-tools/cmdline-tools" "${ANDROID_HOME}/cmdline-tools/latest"; \
    rm /tmp/cmdtools.zip
ENV PATH=${PATH}:${ANDROID_HOME}/cmdline-tools/latest/bin:${ANDROID_HOME}/platform-tools

# 组件版本固定，避免漂移：platform 34 / build-tools 34.0.0 / NDK 26.3（gomobile 交叉编译需要 NDK 的 clang）
ARG NDK_VERSION=26.3.11579264
RUN set -eux; \
    yes | sdkmanager --licenses >/dev/null 2>&1 || true; \
    sdkmanager \
        "platform-tools" \
        "platforms;android-34" \
        "build-tools;34.0.0" >/dev/null; \
    sdkmanager --list_installed

# NDK r26b = 26.3.11579264（source.properties 内 Pkg.Revision 即此值，gomobile 可识别）。
# 腾讯镜像国内直下，避免 sdkmanager 拉 ~1GB NDK 超时；放到 sdkmanager 标准布局目录。
RUN set -eux; \
    curl -fsSL "https://mirrors.cloud.tencent.com/AndroidSDK/android-ndk-r26b-linux.zip" -o /tmp/ndk.zip; \
    unzip -q /tmp/ndk.zip -d /tmp; \
    mkdir -p "${ANDROID_HOME}/ndk"; \
    mv "/tmp/android-ndk-r26b" "${ANDROID_HOME}/ndk/${NDK_VERSION}"; \
    rm /tmp/ndk.zip; \
    test -f "${ANDROID_HOME}/ndk/${NDK_VERSION}/source.properties"
ENV ANDROID_NDK_HOME=${ANDROID_HOME}/ndk/${NDK_VERSION}
ENV ANDROID_NDK_ROOT=${ANDROID_HOME}/ndk/${NDK_VERSION}

# ---- gomobile ----
RUN go install golang.org/x/mobile/cmd/gomobile@latest

# ---- Gradle（项目缺 gradle wrapper，直接用发行版构建）----
ARG GRADLE_VERSION=8.9
RUN set -eux; \
    curl -fsSL "https://mirrors.cloud.tencent.com/gradle/gradle-${GRADLE_VERSION}-bin.zip" -o /tmp/gradle.zip; \
    unzip -q /tmp/gradle.zip -d /opt; \
    rm /tmp/gradle.zip
ENV PATH=/opt/gradle-${GRADLE_VERSION}/bin:${PATH}

# 预热：空目录不构建，仅保证工具可执行
RUN java -version && go version && gradle --version | head -8

WORKDIR /work
