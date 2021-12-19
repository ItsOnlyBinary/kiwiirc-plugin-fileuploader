import { friendlyUrl } from '../../utils/friendly-url';
import { decodeMetadata } from '../../utils/decode-metadata';

export function shareCompletedUploadUrl(kiwiApi) {
    return function handleUploadSuccess(file, response) {
        const url = friendlyUrl(file, response);

        let xhttp = new XMLHttpRequest();
        xhttp.onload = () => {
            if (xhttp.status !== 200) {
                return;
            }

            console.log('uploaded', { file, response });
            console.log('header', xhttp.getResponseHeader('upload-metadata'));
            console.log('metadata', decodeMetadata(xhttp.getResponseHeader('upload-metadata')));
            // emit a global kiwi event
            kiwiApi.emit('fileuploader.uploaded', { url, file });

            // send a message with the url of each successful upload
            const buffer = file.kiwiFileUploaderTargetBuffer;
            const tags = {
                '+kiwiirc.com/fileuploader/file_size': file.size,
                '+kiwiirc.com/fileuploader/file_type': file.type,
            };
            buffer.say(`Uploaded file: ${url}`, { tags });
        };

        xhttp.open('HEAD', url);
        xhttp.send();
    };
}
