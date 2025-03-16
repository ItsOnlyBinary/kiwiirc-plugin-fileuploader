/* global kiwi:true */

const Logger = kiwi.require('libs/Logger');

const log = Logger.namespace('plugin-fileuploader');

export default function acquireExtjwtBeforeUpload(uppy, tokenManager) {
    function handleBeforeUpload(fileIDs) {
        const awaitingPromises = [];

        const files = fileIDs.map((id) => uppy.getFile(id));

        Object.values(files).forEach((fileObj) => {
            const network = fileObj.kiwiFileUploaderTargetBuffer.getNetwork();
            if (network.ircClient.network.options.EXTJWT !== '1') {
                // The network does not support EXTJWT Version 1
                return;
            }

            const maybeToken = tokenManager.get(network);
            if (maybeToken instanceof Promise) {
                awaitingPromises.push(maybeToken);
                fileObj.maybeToken = maybeToken;
            } else {
                uppy.setFileState(fileObj.id, {
                    tus: {
                        headers: {
                            Authorization: maybeToken,
                        },
                    },
                });
            }
        });

        if (awaitingPromises.length) {
            // Wait for the unresolved promises then resume uploading
            return Promise.all(awaitingPromises).then(async () => {
                log.debug('Token acquisition complete. Restarting upload.');

                for (let i = 0; i < files.length; i++) {
                    const fileObj = files[i];
                    uppy.setFileState(fileObj.id, {
                        tus: {
                            headers: {
                                // eslint-disable-next-line no-await-in-loop
                                Authorization: await fileObj.maybeToken,
                            },
                        },
                    });
                    delete fileObj.maybeToken;
                }
            });
        }

        return null;
    }

    return handleBeforeUpload;
}
