"""Original Databend TRUNCATE evidence, distinct from DELETE row counts."""
import json
import verify_agent_databend_transport_fixture as transport

REQUIRED = [('./internal/services/databend', name) for name in [
    'TestDatabendSchemaTruncateOriginalAcknowledgementIdentityAndGuards',
    'TestDatabendSchemaTruncateLostReplyConcurrentIdentityAndNewRows',
    'TestDatabendSchemaTruncateCancellationAndCloseWaitForSettlement',
    'TestDatabendSchemaTruncateNativePrivilegeRefusal',
]] + [('./internal/ui/shell', 'TestDatabendNativePublicSchemaTruncateConfirmationReceiptAndParkedRefresh')]


def verify_truncate(base):
    from verify_agent_databend_schema_fixture import verify_receipt
    def read(name):
        return json.loads((base / name).read_text())
    good = read('schema-truncate.json')
    verify_receipt(good['original']['Receipt'], 'committed', True, 'truncate', 0)
    verify_receipt(good['empty']['Receipt'], 'committed', True, 'truncate', 0)
    if (good['before'] != [['9007199254740993', 'fe00', 'retained'], ['18446744073709551615', '00ff80', 'retained']] or
            good['remaining'] != [['0']] or good['dependent_remaining'] != [['0']] or
            good['restored'] != [['18446744073709551615', '00ff80', 'retained']] or
            any(good.get(k) is not True for k in ['sibling_rejected', 'stale_rejected', 'altered_rejected'])):
        raise RuntimeError('Original truncate data/count/structure guards differ')
    for name in ['truncate_lost62', 'truncate_moved62', 'truncate_appended62']:
        value = read('schema-' + name + '.json')
        lost = name == 'truncate_lost62'
        verify_receipt(value['original']['Receipt'], 'uncertain' if lost else 'committed', False, 'truncate', 0)
        if (value['independent_remaining'] != (None if name == 'truncate_moved62' else [['7']]) or
                value['saved_original'] != ([['1'], ['2']] if name == 'truncate_moved62' else None) or
                value.get('dispatches') != 1 or any(value.get(k) is not True for k in ['read_did_not_settle', 'replay_rejected', 'later_rows_preserved'])):
            raise RuntimeError('Lost/concurrent truncate acquired later rows or wrong original identity')
    for name, closing in [('truncate_cancel62', False), ('truncate_close62', True)]:
        value = read('schema-' + name + '.json')
        verify_receipt(value['original']['Receipt'], 'committed', False, 'truncate', 0)
        if value.get('settlement_waited') is not True or value.get('close') is not closing:
            raise RuntimeError('Original truncate cancellation/close settlement missing')
    denied = read('schema-truncate_denied62.json')
    verify_receipt(denied['original']['Receipt'], 'uncertain', False, 'truncate', 0)
    message = json.loads(denied['original']['Receipt']['NativeJSON'])['Original']['error']['message'].lower()
    if (denied.get('native_unprivileged_user_denied') is not True or denied.get('read_did_not_settle') is not True or
            denied.get('independent_rows') != [['18446744073709551615']] or
            ('permission' not in message and 'privilege' not in message)):
        raise RuntimeError('Native truncate privilege refusal was not established')
    public = [json.loads(line) for line in (base / 'public-truncate.jsonl').read_text().splitlines()]
    if len(public) != 1:
        raise RuntimeError('Public truncate acceptance missing or duplicated')
    value = public[0]
    verify_receipt(value['receipt'], 'committed', True, 'truncate', 0)
    if (value.get('processes') != 1 or value['before'] != [['18446744073709551615', '00ff80']] or
            any(value.get(k) is not True for k in ['public_provider_registered', 'public_context_menu', 'wrong_confirmation_rejected', 'preview_cancelled', 'parked_retained',
                                                  'source_tabs_preserved', 'source_grid_refreshed', 'unrelated_tab_preserved'])):
        raise RuntimeError('Public truncate original scope or grid refresh differs')
    from PIL import Image
    images = {}
    for language in ['zh-Hans', 'en']:
        for width, height in [(1280, 800), (720, 520)]:
            for phase in ['truncate-preview', 'truncate-receipt', 'truncate-complete']:
                path = base / 'workbench' / value['test'] / language / f'{phase}-{width}x{height}.png'
                with Image.open(path) as img:
                    if img.size != (width, height):
                        raise RuntimeError('Public truncate capture size differs')
                    img.verify()
                images[str(path.relative_to(base))] = transport.sha(path.read_bytes())
    return {'public': value, 'image_sha256': images, 'native_ddl_ack_without_delete_count': True, 'unknown_not_replayed': True}
