export class ByteBuf {
    constructor() {
        this.data = undefined;
        this.offset = 0;
    }

    /**
     * Returns the number of bytes this buffer contains.
     *
     * @returns {number} The length of this buffer.
     */
    length() {
        return this.data ? this.data.length : 0;
    }

    /**
     * Returns the current offset of this buffer.
     *
     * @returns {number} The current offset.
     */
    offset() {
        return this.offset;
    }

    /**
     * Resets the offset to 0.
     */
    resetOffset() {
        this.offset = 0;
    }

    /**
     * Appends data to this buffer.
     *
     * @param {Buffer} data - The data to append.
     */
    append(data) {
        if (!data) {
            return;
        }
        this.data = this.data ? Buffer.concat([this.data, data]) : data;
    }

    /**
     * Reads an unsigned byte from this buffer and increment offset by 1.
     *
     * @returns {number|false} The unsigned byte read; or `false` if there is not enough data to read.
     */
    readUnsignedByte() {
        if (this.offset + 1 > this.length()) {
            return false;
        }

        const ret = this.data.readUInt8(this.offset);
        this.offset += 1;
        return ret;
    }

    /**
     * Reads an unsigned short (16-bit) from this buffer and increment offset by 2.
     *
     * @returns {number|false} The unsigned short read; or `false` if there is not enough data to read.
     */
    readUnsignedShort() {
        if (this.offset + 2 > this.length()) {
            return false;
        }

        const ret = this.data.readUInt16BE(this.offset);
        this.offset += 2;
        return ret;
    }

    /**
     * Reads a variable-length integer from this buffer and increment offset.
     *
     * @param {number} [maxBytes=5] - The maximum number of bytes to read.
     * @param {boolean} [skipIncomplete=false] - Whether to skip incomplete values or not.
     * @returns {number|boolean|undefined}
     *  The integer value if successful;
     *  `false` if the maximum length is exceeded;
     *  `false` if the value is incomplete and `skipIncomplete` is `false`;
     *  or `undefined` if the value is incomplete and `skipIncomplete` is `true`.
     */
    readVarInt(maxBytes = 5, skipIncomplete = false) {
        let ret = 0;
        let bytesRead = 0;

        while (true) {
            // read byte
            const b = this.readUnsignedByte();
            if (b === false) {
                return skipIncomplete ? undefined : false;
            }

            // compute result
            ret = ret | ((b & 0x7f) << (bytesRead * 7));
            ++bytesRead;

            // returns when the end is reached
            if ((b & 0x80) === 0) {
                return ret;
            }

            // fail on max length
            if (bytesRead >= maxBytes) {
                return false;
            }
        }
    }

    /**
     * Reads a string from this buffer and increment offset.
     *
     * @param {number} maxPrefixBytes - The maximum number of bytes to read for the string's length prefix.
     * @param {number} maxUtf8Bytes - The maximum number of bytes to read for the string's UTF-8 encoded characters.
     * @returns {string|false} The string read from the buffer; or `false` if the string could not be read.
     */
    readString(maxPrefixBytes, maxUtf8Bytes) {
        const len = this.readVarInt(maxPrefixBytes);
        if (len === false || len < 0 || len > maxUtf8Bytes || this.offset + len > this.length()) {
            return false;
        }

        const ret = this.data.toString('utf8', this.offset, this.offset + len);
        this.offset += len;
        return ret;
    }

    /**
     * Writes a variable-length integer to the buffer.
     *
     * @param {number} value - The integer value to write.
     */
    writeVarInt(value) {
        value |= 0;
        while (true) {
            const b = value & 0x7f;
            value >>>= 7;
            if (value === 0) {
                this.data.writeUInt8(b, this.offset);
                ++this.offset;
                break;
            } else {
                this.data.writeUInt8(b | 0x80, this.offset);
                ++this.offset;
            }
        }
    }

    /**
     * Writes raw data to the buffer.
     *
     * @param {Buffer} data - The data to write to the buffer.
     */
    writeRaw(data) {
        data.copy(this.data, this.offset, 0, data.length);
        this.offset += data.length;
    }
}

/**
 * Returns the number of bytes required to encode a variable-length integer.
 *
 * @param {number} value - The integer value to encode.
 * @returns {number} The number of bytes required to encode the integer value.
 */
export function getVarIntSize(value) {
    value |= 0;
    if (value < 0) return 5;
    if (value < 0x80) return 1;
    if (value < 0x4000) return 2;
    if (value < 0x200000) return 3;
    if (value < 0x10000000) return 4;
    return 5;
}

/**
 * The maximum length of a handshake packet in bytes.
 * @type {number}
 */
const HANDSHAKE_MAX_LEN = 267; // (packetLen)2 + (packetId)1 + (protocolVersion)4 + (hostname)2+255 + (port)2 + (state)1

/**
 * Reads a Minecraft handshake packet from a buffer.
 *
 * @param {ByteBuf} buf - The buffer to read from.
 * @returns {Object|boolean|undefined}
 *  An object containing the protocol version, hostname, port, and state if successful;
 *  `false` if the packet is invalid;
 *  or `undefined` if there is missing data to wait.
 */
export function readHandshake(buf) {
    // read packet len
    const packetLen = buf.readVarInt(2, true);
    if (packetLen === undefined) {
        return; // skip (missing data)
    }
    if (packetLen === false || packetLen > HANDSHAKE_MAX_LEN) {
        return false; // fail (too long packet)
    }
    if (packetLen > buf.length) {
        return; // skip (missing data)
    }

    // read packet id
    const packetId = buf.readVarInt(1);
    if (packetId === false || packetId !== 0) {
        return false; // fail (not an handshake)
    }

    // read protocol version
    const protocolVersion = buf.readVarInt(4);
    if (protocolVersion === false || protocolVersion <= 0) {
        return false; // fail (illegal version)
    }

    // read hostname
    // note: we limit it to 255 utf8 bytes, considering that it contains only ascii characters
    let hostname = buf.readString(2, 255);
    if (hostname === false) {
        return false; // fail (illegal hostname)
    }

    // read port
    const port = buf.readUnsignedShort();
    if (port === false || port <= 0 || port > 65535) {
        return false; // fail (illegal post)
    }

    // read (next_)state
    const state = buf.readVarInt(1);
    if (state !== 1 && state !== 2) {
        return false; // fail (illegal state)
    }

    // trim clients/mods suffix from host (everything after \0) then returns
    const hostnameEnd = hostname.indexOf('\0');
    if (hostnameEnd !== -1) {
        hostname = hostname.slice(0, hostnameEnd);
    }
    return {protocolVersion, hostname, port, state};
}

export function writeStringPacket(packetId, str) {
    const strBuf = Buffer.from(str, 'utf8');
    const packetLen = getVarIntSize(packetId) + getVarIntSize(strBuf.byteLength) + strBuf.byteLength;

    const buf = new ByteBuf();
    buf.append(Buffer.alloc(getVarIntSize(packetLen) + packetLen));
    buf.writeVarInt(packetLen);
    buf.writeVarInt(packetId);
    buf.writeVarInt(strBuf.byteLength);
    buf.writeRaw(strBuf);
    return buf.data;
}

export const PACKET_PONG = (() => {
    const buf = Buffer.alloc(10);
    buf.writeUInt8(9, 0); // packet len
    buf.writeUInt8(1, 1); // packet id
    buf.writeUInt32BE(0, 2); // client time
    buf.writeUInt32BE(818, 6); // client time
    return buf;
})();
