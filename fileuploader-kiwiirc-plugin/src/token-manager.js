/* global kiwi:true */
import isPromise from 'p-is-promise';
import CacheLoader from './cache-loader';

const seconds = 1000;
const minutes = 60 * seconds;

const ErrExtJwtUnsupported = new Error('EXTJWT unsupported on this server/gateway');

const UNSUPPORTED_TTL = 5 * minutes;

export default class TokenManager {
    constructor() {
        this.unsupportedNetworks = new Map();
        this.requestToken = this.requestToken.bind(this); // ?!?!
        this.cacheLoader = new CacheLoader(this.requestToken, TokenManager.assertValid);
    }

    get(network) {
        if (this.unsupportedNetworks.has(network)) {
            if (new Date() - this.unsupportedNetworks.get(network) < UNSUPPORTED_TTL) {
                return false; // don't retry EXTJWT on unsupported servers
            }
        }

        const maybePromise = this.cacheLoader.get(network);

        if (isPromise(maybePromise)) {
            const tokenRecordPromise = maybePromise;
            return tokenRecordPromise
                .then(tokenRecord => tokenRecord.token)
                .catch(err => {
                    if (err === ErrExtJwtUnsupported) {
                        return false;
                    }
                    throw err;
                });
        }

        const tokenRecord = maybePromise;
        return tokenRecord.token;
    }

    async requestToken(network) {
        const thisTokenManager = this;

        const respPromise = awaitToken({ timeout: 10 * seconds });
        network.ircClient.raw('EXTJWT');

        let token;
        try {
            token = await respPromise;
        } catch (err) {
            if (err === ErrExtJwtUnsupported) {
                const unsupportedAt = new Date();
                thisTokenManager.unsupportedNetworks.set(network, unsupportedAt);
                console.debug('Network does not support EXTJWT:', network);
            }
            throw err;
        }

        const acquiredAt = new Date();
        return { token, acquiredAt };
    }

    static assertValid(tokenRecord) {
        // eslint-disable-next-line no-unused-vars
        const { token, acquiredAt } = tokenRecord;
        const now = new Date();
        if (now - acquiredAt > 15 * seconds) {
            throw new Error(`Stale token: ${(now - acquiredAt) / 1000} seconds age exceeds 15 second limit`);
        }
    }
}

function awaitToken({ timeout } = { timeout: undefined }) {
    return new Promise((resolve, reject) => {
        let timeoutHandle;

        const removeEvents = () => {
            if (timeoutHandle) {
                clearTimeout(timeoutHandle);
            }
            kiwi.off('irc.raw.421', rawHandler);
            kiwi.off('irc.raw.EXTJWT', rawHandler);
        };

        const rawHandler = (command, event) => {
            if (command === '421') {
                if (event.params[1].toUpperCase() === 'EXTJWT') {
                    event.handled = true;
                    removeEvents();
                    reject(ErrExtJwtUnsupported);
                }
                return;
            }

            event.handled = true;
            const token = event.params[event.params.length - 1];
            removeEvents();
            resolve(token);
        };

        if (timeout) {
            timeoutHandle = setTimeout(() => {
                removeEvents();
                reject(new Error('Timeout expired'));
            }, timeout);
        }

        kiwi.on('irc.raw.421', rawHandler);
        kiwi.on('irc.raw.EXTJWT', rawHandler);
    });
}
