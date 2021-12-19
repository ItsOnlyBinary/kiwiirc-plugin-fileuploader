import { friendlyUrl } from '../../utils/friendly-url';

export function shareCompletedUploadUrl(kiwiApi) {
    return function handleUploadSuccess(file, response) {
        console.log('uploaded', { file, response });
        const url = friendlyUrl(file, response);

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
}
