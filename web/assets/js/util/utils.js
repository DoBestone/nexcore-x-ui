class HttpUtil {
    static _handleMsg(msg) {
        if (!(msg instanceof Msg)) {
            return;
        }
        if (msg.msg === "") {
            return;
        }
        if (msg.success) {
            Vue.prototype.$message.success(msg.msg);
        } else {
            // Translate machine-readable API codes to friendly Chinese text
            // before showing. humanizeApiError leaves unknown messages alone,
            // so legacy plain-Chinese errors still display unchanged.
            try {
                msg.msg = humanizeApiError(msg.msg);
            } catch (e) { /* defensive: never block error toast */ }
            Vue.prototype.$message.error(msg.msg);
        }
    }

    static _respToMsg(resp) {
        const data = resp.data;
        if (data == null) {
            return new Msg(true);
        } else if (typeof data === 'object') {
            if (data.hasOwnProperty('success')) {
                return new Msg(data.success, data.msg, data.obj);
            } else {
                return data;
            }
        } else {
            return new Msg(false, 'unknown data:', data);
        }
    }

    static async get(url, data, options) {
        let msg;
        try {
            const resp = await axios.get(url, data, options);
            msg = this._respToMsg(resp);
        } catch (e) {
            msg = new Msg(false, e.toString());
        }
        this._handleMsg(msg);
        return msg;
    }

    static async post(url, data, options) {
        let msg;
        try {
            const resp = await axios.post(url, data, options);
            msg = this._respToMsg(resp);
        } catch (e) {
            msg = new Msg(false, e.toString());
        }
        this._handleMsg(msg);
        return msg;
    }

    static async delete(url, options) {
        let msg;
        try {
            const resp = await axios.delete(url, options);
            msg = this._respToMsg(resp);
        } catch (e) {
            msg = new Msg(false, e.toString());
        }
        this._handleMsg(msg);
        return msg;
    }

    static async postWithModal(url, data, modal) {
        if (modal) {
            modal.loading(true);
        }
        const msg = await this.post(url, data);
        if (modal) {
            modal.loading(false);
            if (msg instanceof Msg && msg.success) {
                modal.close();
            }
        }
        return msg;
    }
}

class PromiseUtil {

    static async sleep(timeout) {
        await new Promise(resolve => {
            setTimeout(resolve, timeout)
        });
    }

}

// humanizeApiError 把后端抛出的 "<操作>失败: <code>: <details>" 字符串翻译
// 成对用户更友好的中文文案。后端为了机器可读保留了错误码(供业务系统 switch),
// 前端为了用户体验把已知 code 转成解释 + 保留原文 details 作为定位线索。
//
// 使用:`HttpUtil` 的回调里 _handleMsg 会自动调它,任何调用方拿到的 msg.msg
// 都是处理后的中文。无 code 匹配时原样返回(降级到 v1.0.9 之前的行为)。
const apiErrorCodeMap = {
    xray_config_invalid:   'xray 校验候选配置失败,字段非法',
    protocol_singleton:    '该协议(VLESS/VMess/Trojan/SS-2022)全局只允许一个入站,请编辑现有入站添加客户端',
    invalid_body:          '请求体格式错误',
    invalid_json:          '请求体不是合法 JSON',
    invalid_id:            'id 格式错误',
    invalid_port:          '端口必须是 1~65535',
    inbound_not_found:     '该入站不存在',
    create_failed:         '创建失败',
    update_failed:         '更新失败',
    delete_failed:         '删除失败',
    reset_failed:          '重置失败',
    host_required:         '需要传 host 参数(节点对外可达地址)',
    client_identifier_required: '客户端 email 字段必填',
    client_not_found:      '客户端不存在',
    client_duplicate:      '该 email 在此入站已存在',
    unsupported_protocol:  '该协议不支持客户端管理',
    token_not_found:       'token 不存在',
    revoke_failed:         '撤销 token 失败',
    rename_failed:         '重命名失败',
    cert_save_failed:      '证书保存失败(PEM 解析或落盘错误)',
    xray_restart_failed:   'xray 重启失败',
    update_check_failed:   '检查更新失败(GitHub API 不可达)',
    update_apply_failed:   '在线升级失败',
    db_error:              '数据库错误',
    rotate_failed:         '生成 token 失败',
};

function humanizeApiError(raw) {
    if (typeof raw !== 'string' || raw.length === 0) return raw;
    // 后端常见格式 "<操作>失败: <code>: <details>" 或 "<code>: <details>"。
    // 用第一个含下划线的 token 当 code 候选,其后做 details。
    const m = raw.match(/(?:.*?[::]\s*)?([a-z][a-z0-9_]+)(?:\s*[::]\s*([\s\S]*))?/);
    if (!m) return raw;
    const code = m[1];
    const details = (m[2] || '').trim();
    const human = apiErrorCodeMap[code];
    if (!human) return raw;
    return details ? `${human}(${details})` : human;
}

const seq = [
    'a', 'b', 'c', 'd', 'e', 'f', 'g',
    'h', 'i', 'j', 'k', 'l', 'm', 'n',
    'o', 'p', 'q', 'r', 's', 't',
    'u', 'v', 'w', 'x', 'y', 'z',
    '0', '1', '2', '3', '4', '5', '6', '7', '8', '9',
    'A', 'B', 'C', 'D', 'E', 'F', 'G',
    'H', 'I', 'J', 'K', 'L', 'M', 'N',
    'O', 'P', 'Q', 'R', 'S', 'T',
    'U', 'V', 'W', 'X', 'Y', 'Z'
];

class RandomUtil {

    static randomIntRange(min, max) {
        return parseInt(Math.random() * (max - min) + min, 10);
    }

    static randomInt(n) {
        return this.randomIntRange(0, n);
    }

    static randomSeq(count) {
        let str = '';
        for (let i = 0; i < count; ++i) {
            str += seq[this.randomInt(62)];
        }
        return str;
    }

    static randomLowerAndNum(count) {
        let str = '';
        for (let i = 0; i < count; ++i) {
            str += seq[this.randomInt(36)];
        }
        return str;
    }

    static randomMTSecret() {
        let str = '';
        for (let i = 0; i < 32; ++i) {
            let index = this.randomInt(16);
            if (index <= 9) {
                str += index;
            } else {
                str += seq[index - 10];
            }
        }
        return str;
    }

    static randomUUID() {
        let d = new Date().getTime();
        return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, function (c) {
            let r = (d + Math.random() * 16) % 16 | 0;
            d = Math.floor(d / 16);
            return (c === 'x' ? r : (r & 0x7 | 0x8)).toString(16);
        });
    }
}

class ObjectUtil {

    static getPropIgnoreCase(obj, prop) {
        for (const name in obj) {
            if (!obj.hasOwnProperty(name)) {
                continue;
            }
            if (name.toLowerCase() === prop.toLowerCase()) {
                return obj[name];
            }
        }
        return undefined;
    }

    static deepSearch(obj, key) {
        if (obj instanceof Array) {
            for (let i = 0; i < obj.length; ++i) {
                if (this.deepSearch(obj[i], key)) {
                    return true;
                }
            }
        } else if (obj instanceof Object) {
            for (let name in obj) {
                if (!obj.hasOwnProperty(name)) {
                    continue;
                }
                if (this.deepSearch(obj[name], key)) {
                    return true;
                }
            }
        } else {
            return obj.toString().indexOf(key) >= 0;
        }
        return false;
    }

    static isEmpty(obj) {
        return obj === null || obj === undefined || obj === '';
    }

    static isArrEmpty(arr) {
        return !this.isEmpty(arr) && arr.length === 0;
    }

    static copyArr(dest, src) {
        dest.splice(0);
        for (const item of src) {
            dest.push(item);
        }
    }

    static clone(obj) {
        let newObj;
        if (obj instanceof Array) {
            newObj = [];
            this.copyArr(newObj, obj);
        } else if (obj instanceof Object) {
            newObj = {};
            for (const key of Object.keys(obj)) {
                newObj[key] = obj[key];
            }
        } else {
            newObj = obj;
        }
        return newObj;
    }

    static deepClone(obj) {
        let newObj;
        if (obj instanceof Array) {
            newObj = [];
            for (const item of obj) {
                newObj.push(this.deepClone(item));
            }
        } else if (obj instanceof Object) {
            newObj = {};
            for (const key of Object.keys(obj)) {
                newObj[key] = this.deepClone(obj[key]);
            }
        } else {
            newObj = obj;
        }
        return newObj;
    }

    static cloneProps(dest, src, ...ignoreProps) {
        if (dest == null || src == null) {
            return;
        }
        const ignoreEmpty = this.isArrEmpty(ignoreProps);
        for (const key of Object.keys(src)) {
            if (!src.hasOwnProperty(key)) {
                continue;
            } else if (!dest.hasOwnProperty(key)) {
                continue;
            } else if (src[key] === undefined) {
                continue;
            }
            if (ignoreEmpty) {
                dest[key] = src[key];
            } else {
                let ignore = false;
                for (let i = 0; i < ignoreProps.length; ++i) {
                    if (key === ignoreProps[i]) {
                        ignore = true;
                        break;
                    }
                }
                if (!ignore) {
                    dest[key] = src[key];
                }
            }
        }
    }

    static delProps(obj, ...props) {
        for (const prop of props) {
            if (prop in obj) {
                delete obj[prop];
            }
        }
    }

    static execute(func, ...args) {
        if (!this.isEmpty(func) && typeof func === 'function') {
            func(...args);
        }
    }

    static orDefault(obj, defaultValue) {
        if (obj == null) {
            return defaultValue;
        }
        return obj;
    }

    static equals(a, b) {
        for (const key in a) {
            if (!a.hasOwnProperty(key)) {
                continue;
            }
            if (!b.hasOwnProperty(key)) {
                return false;
            } else if (a[key] !== b[key]) {
                return false;
            }
        }
        return true;
    }

}
