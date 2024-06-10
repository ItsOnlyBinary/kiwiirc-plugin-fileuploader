/* global kiwi:true */

const Logger = kiwi.require('libs/Logger');

const log = Logger.namespace('plugin-fileuploader');

export default function acquireExtjwtBeforeUpload(uppy, tokenManager) {
    function handleBeforeUpload(fileIDs) {
        const awaitingPromises = new Set();

        const files = fileIDs.map((id) => uppy.getFile(id));

        Object.values(files).forEach((fileObj) => {
            const network = fileObj.kiwiFileUploaderTargetBuffer.getNetwork();
            if (network.ircClient.network.options.EXTJWT !== '1') {
                // The network does not support EXTJWT Version 1
                return;
            }

            const maybeToken = tokenManager.get(network);
            if (maybeToken instanceof Promise) {
                awaitingPromises.add(maybeToken);
            } else {
                fileObj.meta.extjwt = maybeToken;
            }
        });

        if (awaitingPromises.size) {
            // Wait for the unresolved promises then resume uploading
            return Promise.all(awaitingPromises.values()).then(() => {
                log.debug('Token acquisition complete. Restarting upload.');
            });
        }

        return null;
    }

    return handleBeforeUpload;
}
