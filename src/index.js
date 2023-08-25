import 'dotenv/config';
import {createServer} from 'node:net';
import {readFileSync} from 'node:fs';
import {log, formatAddress} from './utils.js';
import {ByteBuf, readHandshake, writeStringPacket, PACKET_PONG} from './mc-protocol.js';

const HANDSHAKE_TIMEOUT = 2000; // ms

function main() {
    const server = createServer();

    // Add server lifecycle logging
    server.on('listening', () => {
        const {address, port} = server.address();
        log(`Listening on ${formatAddress(address)}:${port}`);
    });
    server.on('error', (err) => {
        log('Server Error:', err);
    });

    // Add socket connection handler
    server.on('connection', handleSocket);

    // Start listening
    const listenOpts = {
        host: process.env.LISTEN_HOST || undefined,
        port: parseInt(process.env.LISTEN_PORT) || 25565,
        backlog: parseInt(process.env.LISTEN_BACKLOG) || undefined,
    };
    const listenErrorHandler = () => process.exit(1);
    server.on('error', listenErrorHandler);
    server.listen(listenOpts, () => server.off('error', listenErrorHandler));
}

/**
 * Handles a socket connection.
 *
 * @param {net.Socket} socket - The client socket.
 */
function handleSocket(socket) {
    const name = `[${formatAddress(socket.remoteAddress, false)}]:${socket.remotePort}`;

    // Add socket lifecycle logging
    log(`${name} connected`);
    let answered = false;
    let sockerErr = undefined;
    socket.on('error', (err) => {
        // log('Socket Error:', err); // debug
        if (!sockerErr) {
            sockerErr = err;
        }
    });
    socket.on('close', () => {
        if (sockerErr) {
            log(`${name} disconnected with error: ${sockerErr.message}`);
        } else if (!answered) {
            log(`${name} disconnected before receiving response`);
        } else {
            log(`${name} disconnected successfully`);
        }
    });

    // Ensure that the socket is destroyed immediately on error
    socket.on('error', () => socket.destroy());

    // Set a strict timeout for the handshake
    const timeoutTask = setTimeout(() => socket.destroy(new Error('Timeout')), HANDSHAKE_TIMEOUT);
    socket.on('close', () => clearTimeout(timeoutTask));

    // Add data handler
    const buf = new ByteBuf();
    socket.on('data', (data) => {
        if (socket.readyState !== 'open') {
            // always skip data after a call to socket.end()
            // without this, already received data sometimes continues to be read for a short time
            return;
        }
        if (answered) {
            return; // skip (already answered)
        }

        // Read the handshake
        buf.append(data);
        buf.resetOffset();
        const handshake = readHandshake(buf);
        if (handshake === undefined) {
            return; // skip (missing data)
        }
        if (handshake === false) {
            // fail (illegal handshake)
            socket.destroy(new Error('Illegal handshake'));
            return;
        }

        log(`${name} sent handshake: ${JSON.stringify(handshake)}`);

        // Respond and close the socket
        answered = true;
        if (handshake.state === 2) {
            socket.end(getKickPacket(handshake));
        } else {
            socket.write(getServerListPacket(handshake));
            socket.end(PACKET_PONG);
        }
    });
}

function parseChatComponent(str) {
    if (str.charAt(0) === '{') {
        try {
            return JSON.parse(str);
        } catch (ignored) {}
    }
    return {text: str};
}

function readFavicon(strOrPath) {
    if (!strOrPath) {
        return undefined;
    }

    if (strOrPath.startsWith('data:')) {
        return strOrPath;
    }

    try {
        const data = readFileSync(strOrPath, {encoding: 'base64'});
        return `data:image/png;base64,${data}`;
    } catch (err) {
        log(`Cannot read favicon: ${err.message}`);
        return undefined;
    }
}

const getKickPacket = (() => {
    const packet = writeStringPacket(
        0,
        JSON.stringify(parseChatComponent(process.env.KICK_MESSAGE || '§cNot available'))
    );
    return (handshake) => packet;
})();

const getServerListPacket = (() => {
    // TODO: Allow PROTOCOL_VERSION=auto to copy the client's one
    const packet = writeStringPacket(
        0,
        JSON.stringify({
            version: {
                name: process.env.PROTOCOL_NAME || '',
                protocol: parseInt(process.env.PROTOCOL_VERSION) || 0,
            },
            players: {
                max: parseInt(process.env.MAX_PLAYERS) || 0,
                online: parseInt(process.env.ONLINE_PLAYERS) || 0,
                sample: [],
            },
            description: parseChatComponent(process.env.MOTD || '§eHello World!'),
            favicon: readFavicon(process.env.FAVICON),
        })
    );
    return (handshake) => packet;
})();

main();
