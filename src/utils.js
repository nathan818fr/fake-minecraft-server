/**
 * Logs a message to the console.
 *
 * @param {string} message - The message to log.
 * @param {...any} args - Additional arguments to log.
 */
export function log(message, ...args) {
    const now = new Date();
    console.log(`[${now.toISOString()}] ${message}`, ...args);
}

/**
 * Formats the given IP address by removing the IPv6 encapsulation or enclosing IPv6 in square brackets.
 *
 * @param {string|undefined} address - The IP address to format.
 * @param {boolean} [enclosedIpv6=true] - Whether to enclose IPv6 addresses in square brackets.
 * @returns {string} The formatted IP address.
 */
export function formatAddress(address, enclosedIpv6 = true) {
    if (!address) {
        return 'undefined';
    }
    if (address.startsWith('::ffff:')) {
        return address.slice(7);
    }
    if (enclosedIpv6 && address.includes(':')) {
        return '[' + address + ']';
    }
    return address;
}
