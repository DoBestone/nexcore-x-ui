class User {

    constructor() {
        this.username = "";
        this.password = "";
    }
}

class Msg {

    constructor(success, msg, obj) {
        this.success = false;
        this.msg = "";
        this.obj = null;

        if (success != null) {
            this.success = success;
        }
        if (msg != null) {
            this.msg = msg;
        }
        if (obj != null) {
            this.obj = obj;
        }
    }
}

class DBInbound {

    constructor(data) {
        this.id = 0;
        this.userId = 0;
        this.up = 0;
        this.down = 0;
        this.total = 0;
        this.remark = "";
        this.enable = true;
        this.expiryTime = 0;

        this.listen = "";
        this.port = 0;
        this.protocol = "";
        this.settings = "";
        this.streamSettings = "";
        this.tag = "";
        this.sniffing = "";

        if (data == null) {
            return;
        }
        ObjectUtil.cloneProps(this, data);
    }

    get totalGB() {
        return toFixed(this.total / ONE_GB, 2);
    }

    set totalGB(gb) {
        this.total = toFixed(gb * ONE_GB, 0);
    }

    get isVMess() {
        return this.protocol === Protocols.VMESS;
    }

    get isVLess() {
        return this.protocol === Protocols.VLESS;
    }

    get isTrojan() {
        return this.protocol === Protocols.TROJAN;
    }

    get isSS() {
        return this.protocol === Protocols.SHADOWSOCKS;
    }

    get isSocks() {
        return this.protocol === Protocols.SOCKS;
    }

    get isHTTP() {
        return this.protocol === Protocols.HTTP;
    }

    // v1.1.2:多用户协议判定。VLESS/VMess/Trojan 100%,Shadowsocks 看
    // method 是不是 2022-blake3-*(legacy aead 单 password 单 user)。
    // 列表页用它来把入站拆成两个卡片,不同表头不同操作菜单。
    get isMultiUser() {
        if (this.isVLess || this.isVMess || this.isTrojan) return true;
        if (this.isSS) {
            try {
                const s = JSON.parse(this.settings || '{}');
                return typeof s.method === 'string' && s.method.startsWith('2022-blake3-');
            } catch (e) { return false; }
        }
        return false;
    }

    get address() {
        let address = location.hostname;
        if (!ObjectUtil.isEmpty(this.listen) && this.listen !== "0.0.0.0") {
            address = this.listen;
        }
        return address;
    }

    get _expiryTime() {
        if (this.expiryTime === 0) {
            return null;
        }
        return moment(this.expiryTime);
    }

    set _expiryTime(t) {
        if (t == null) {
            this.expiryTime = 0;
        } else {
            this.expiryTime = t.valueOf();
        }
    }

    get isExpiry() {
        return this.expiryTime < new Date().getTime();
    }

    toInbound() {
        let settings = {};
        if (!ObjectUtil.isEmpty(this.settings)) {
            settings = JSON.parse(this.settings);
        }

        let streamSettings = {};
        if (!ObjectUtil.isEmpty(this.streamSettings)) {
            streamSettings = JSON.parse(this.streamSettings);
        }

        let sniffing = {};
        if (!ObjectUtil.isEmpty(this.sniffing)) {
            sniffing = JSON.parse(this.sniffing);
        }
        const config = {
            port: this.port,
            listen: this.listen,
            protocol: this.protocol,
            settings: settings,
            streamSettings: streamSettings,
            tag: this.tag,
            sniffing: sniffing,
        };
        return Inbound.fromJson(config);
    }

    hasLink() {
        switch (this.protocol) {
            case Protocols.VMESS:
            case Protocols.VLESS:
            case Protocols.TROJAN:
            case Protocols.SHADOWSOCKS:
                return true;
            default:
                return false;
        }
    }

    genLink() {
        const inbound = this.toInbound();
        return inbound.genLink(this.address, this.remark);
    }
}

class AllSetting {

    constructor(data) {
        this.webListen = "";
        this.webPort = 54321;
        this.webCertFile = "";
        this.webKeyFile = "";
        this.webBasePath = "/";
        this.tgBotEnable = false;
        this.tgBotToken = "";
        this.tgBotChatId = 0;
        this.tgRunTime = "";
        this.xrayTemplateConfig = "";

        this.timeLocation = "Asia/Shanghai";

        if (data == null) {
            return
        }
        ObjectUtil.cloneProps(this, data);
    }

    equals(other) {
        return ObjectUtil.equals(this, other);
    }
}